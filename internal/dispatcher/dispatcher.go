// Package dispatcher is the central tool router for pagefault.
//
// The dispatcher holds the registered backends, named contexts, filter
// pipeline, and audit logger. Tool handlers call dispatcher methods to
// perform the actual work — the dispatcher is where filtering and auditing
// happen uniformly for every tool.
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jet/pagefault/internal/audit"
	"github.com/jet/pagefault/internal/backend"
	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/filter"
	"github.com/jet/pagefault/internal/model"
	"github.com/jet/pagefault/internal/task"
)

// ToolDispatcher routes tool calls to backends, applies filters, and audits
// every call. It is safe for concurrent use — all backend implementations
// are expected to be concurrency-safe.
type ToolDispatcher struct {
	backends        map[string]backend.Backend
	backendsOrdered []backend.Backend
	schemeToBackend map[string]backend.Backend

	contexts        map[string]config.ContextConfig
	contextsOrdered []config.ContextConfig

	filter   *filter.CompositeFilter
	auditLog audit.Logger
	toolsCfg config.ToolsConfig
	// tasks is the 0.10.0 async task manager. Every DeepRetrieve /
	// DelegateWrite call flows through it (the subagent goroutine
	// runs on a detached context so the caller's HTTP request can
	// disconnect without killing the spawn). Never nil — New()
	// constructs a default manager when Options.Tasks is not set.
	tasks *task.Manager
}

// Options bundles the dependencies required to construct a ToolDispatcher.
type Options struct {
	Backends []backend.Backend
	Contexts []config.ContextConfig
	Filter   *filter.CompositeFilter
	Audit    audit.Logger
	Tools    config.ToolsConfig
	// Tasks is the async task manager used by DeepRetrieve /
	// DelegateWrite. If nil, New() constructs one with default
	// config so test code and minimal embedders get the async
	// behaviour without extra wiring.
	Tasks *task.Manager
}

// New constructs a ToolDispatcher from Options. It validates that backend
// names are unique and builds a scheme→backend lookup where possible.
func New(opts Options) (*ToolDispatcher, error) {
	if opts.Filter == nil {
		opts.Filter = filter.NewCompositeFilter()
	}
	if opts.Audit == nil {
		opts.Audit = audit.NopLogger{}
	}
	if opts.Tasks == nil {
		opts.Tasks = task.NewManager(task.Config{})
	}

	d := &ToolDispatcher{
		backends:        make(map[string]backend.Backend, len(opts.Backends)),
		schemeToBackend: make(map[string]backend.Backend),
		contexts:        make(map[string]config.ContextConfig, len(opts.Contexts)),
		contextsOrdered: append([]config.ContextConfig(nil), opts.Contexts...),
		filter:          opts.Filter,
		auditLog:        opts.Audit,
		toolsCfg:        opts.Tools,
		tasks:           opts.Tasks,
	}

	for _, b := range opts.Backends {
		name := b.Name()
		if _, dup := d.backends[name]; dup {
			return nil, fmt.Errorf("dispatcher: duplicate backend name %q", name)
		}
		d.backends[name] = b
		d.backendsOrdered = append(d.backendsOrdered, b)

		// Backends with a URI scheme accessor (e.g., FilesystemBackend)
		// register themselves for scheme routing.
		if sb, ok := b.(schemeBackend); ok {
			sch := sb.URIScheme()
			if existing, dup := d.schemeToBackend[sch]; dup {
				return nil, fmt.Errorf("dispatcher: backends %q and %q both claim scheme %q",
					existing.Name(), name, sch)
			}
			d.schemeToBackend[sch] = b
		}
	}

	for _, c := range opts.Contexts {
		if _, dup := d.contexts[c.Name]; dup {
			return nil, fmt.Errorf("dispatcher: duplicate context name %q", c.Name)
		}
		d.contexts[c.Name] = c
		for _, s := range c.Sources {
			if _, ok := d.backends[s.Backend]; !ok {
				return nil, fmt.Errorf("dispatcher: context %q references unknown backend %q", c.Name, s.Backend)
			}
		}
	}

	return d, nil
}

// schemeBackend is an optional interface implemented by backends that expose
// a URI scheme for routing.
type schemeBackend interface {
	URIScheme() string
}

// ContextSummary is the lightweight response shape for ListContexts.
type ContextSummary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Backend returns a registered backend by name.
func (d *ToolDispatcher) Backend(name string) (backend.Backend, bool) {
	b, ok := d.backends[name]
	return b, ok
}

// ToolEnabled reports whether a named tool is enabled by config.
func (d *ToolDispatcher) ToolEnabled(name string) bool {
	return d.toolsCfg.Enabled(name)
}

// ───────────────────── list_contexts ─────────────────────

// ListContexts returns every configured context name and description.
func (d *ToolDispatcher) ListContexts(ctx context.Context, caller model.Caller) ([]ContextSummary, error) {
	start := time.Now()
	out := make([]ContextSummary, 0, len(d.contextsOrdered))
	for _, c := range d.contextsOrdered {
		out = append(out, ContextSummary{Name: c.Name, Description: c.Description})
	}
	d.auditLog.Log(audit.NewEntry(caller, "pf_maps", nil, start, len(out), nil))
	return out, nil
}

// ───────────────────── get_context ─────────────────────

// SkippedSource records a context source that was omitted during GetContext
// and the reason why. Callers should surface this so users can tell why a
// context bundle is incomplete.
type SkippedSource struct {
	URI    string `json:"uri"`
	Reason string `json:"reason"`
}

// GetContext loads the named context, concatenates its sources (applying
// filters), and truncates to max_size if necessary. Any sources that were
// dropped along the way (blocked by filters, backend read error) are
// returned in the skipped slice — the caller is responsible for surfacing
// them to the user.
//
// The resolved format (after applying the request override → context
// default → "markdown" fallback chain) is returned so callers can echo
// the actual format used in their response envelope, not the format the
// caller *asked* for.
//
// format overrides the context's configured format when non-empty.
// Supported formats:
//
//   - "markdown" (default): per-source `# {uri}` headers, joined with
//     `\n\n---\n\n`. Truncated at the byte level (rune-aligned) if the
//     joined output exceeds cfg.MaxSize.
//   - "markdown-with-metadata": same as markdown but each header is
//     followed by a blockquote summarizing content-type and tags.
//   - "json": a structured JSON document
//     `{"name":..., "sources":[{"uri","content_type","content","tags","metadata"}]}`.
//     Sources whose inclusion would push the marshaled output past
//     cfg.MaxSize are dropped from the tail and recorded in the skipped
//     slice with reason `max_size budget exceeded` — ensures the emitted
//     JSON remains valid.
func (d *ToolDispatcher) GetContext(ctx context.Context, name string, format string, caller model.Caller) (string, string, []SkippedSource, error) {
	start := time.Now()

	var out string
	var skipped []SkippedSource
	var err error
	defer func() {
		d.auditLog.Log(audit.NewEntry(caller, "pf_load",
			map[string]any{"name": name, "format": format, "skipped": len(skipped)},
			start, len(out), err))
	}()

	cfg, ok := d.contexts[name]
	if !ok {
		err = fmt.Errorf("%w: %q", model.ErrContextNotFound, name)
		return "", "", nil, err
	}

	if format == "" {
		format = cfg.Format
	}
	if format == "" {
		format = "markdown"
	}
	switch format {
	case "markdown", "markdown-with-metadata", "json":
	default:
		err = fmt.Errorf("%w: unknown context format %q", model.ErrInvalidRequest, format)
		return "", "", nil, err
	}

	addSkip := func(uri, reason string) {
		skipped = append(skipped, SkippedSource{URI: uri, Reason: reason})
		slog.Warn("get_context: source skipped",
			"context", name,
			"uri", uri,
			"reason", reason,
			"caller", caller.ID,
		)
	}

	type loadedSource struct {
		URI         string
		ContentType string
		Content     string
		Tags        []string
		Metadata    map[string]any
	}

	var loaded []loadedSource
	for _, src := range cfg.Sources {
		be, ok := d.backends[src.Backend]
		if !ok {
			// Context references a backend we don't have — configuration
			// error, fail hard instead of silently dropping.
			err = fmt.Errorf("%w: context %q references unknown backend %q", model.ErrBackendNotFound, name, src.Backend)
			return "", "", nil, err
		}
		if !d.filter.AllowURI(src.URI, &caller) {
			addSkip(src.URI, "blocked by uri filter")
			continue
		}
		res, rerr := be.Read(ctx, src.URI)
		if rerr != nil {
			// Per-source errors don't break the whole bundle — a missing
			// file shouldn't poison the context — but we record what we
			// dropped so the caller isn't left guessing.
			addSkip(src.URI, fmt.Sprintf("read error: %s", rerr.Error()))
			continue
		}
		tags := resourceTags(res)
		if !d.filter.AllowTags(res.URI, tags, &caller) {
			addSkip(res.URI, "blocked by tag filter")
			continue
		}
		content := d.filter.FilterContent(res.Content, res.URI)
		loaded = append(loaded, loadedSource{
			URI:         res.URI,
			ContentType: res.ContentType,
			Content:     content,
			Tags:        tags,
			Metadata:    res.Metadata,
		})
	}

	switch format {
	case "markdown":
		parts := make([]string, 0, len(loaded))
		for _, s := range loaded {
			parts = append(parts, fmt.Sprintf("# %s\n\n%s", s.URI, s.Content))
		}
		joined := strings.Join(parts, "\n\n---\n\n")
		if cfg.MaxSize > 0 && len(joined) > cfg.MaxSize {
			cut := cfg.MaxSize
			for cut > 0 && !utf8.RuneStart(joined[cut]) {
				cut--
			}
			joined = joined[:cut] + "\n\n...[truncated]"
		}
		out = joined

	case "markdown-with-metadata":
		parts := make([]string, 0, len(loaded))
		for _, s := range loaded {
			meta := renderMarkdownMetadata(s.ContentType, s.Tags)
			parts = append(parts, fmt.Sprintf("# %s\n\n%s%s", s.URI, meta, s.Content))
		}
		joined := strings.Join(parts, "\n\n---\n\n")
		if cfg.MaxSize > 0 && len(joined) > cfg.MaxSize {
			cut := cfg.MaxSize
			for cut > 0 && !utf8.RuneStart(joined[cut]) {
				cut--
			}
			joined = joined[:cut] + "\n\n...[truncated]"
		}
		out = joined

	case "json":
		type jsonSource struct {
			URI         string         `json:"uri"`
			ContentType string         `json:"content_type,omitempty"`
			Content     string         `json:"content"`
			Tags        []string       `json:"tags,omitempty"`
			Metadata    map[string]any `json:"metadata,omitempty"`
		}
		type jsonBundle struct {
			Name    string       `json:"name"`
			Sources []jsonSource `json:"sources"`
		}
		bundle := jsonBundle{Name: name, Sources: make([]jsonSource, 0, len(loaded))}
		for _, s := range loaded {
			bundle.Sources = append(bundle.Sources, jsonSource{
				URI:         s.URI,
				ContentType: s.ContentType,
				Content:     s.Content,
				Tags:        s.Tags,
				Metadata:    s.Metadata,
			})
		}

		// Enforce max_size in JSON mode by dropping tail sources — byte
		// truncation would produce invalid JSON, and truncating a single
		// source's content mid-string is surprising. Dropping whole
		// sources keeps the document valid and reports the omission via
		// skipped_sources.
		marshaled, merr := json.Marshal(bundle)
		if merr != nil {
			err = fmt.Errorf("get_context: marshal json: %w", merr)
			return "", "", nil, err
		}
		if cfg.MaxSize > 0 {
			for len(marshaled) > cfg.MaxSize && len(bundle.Sources) > 0 {
				dropped := bundle.Sources[len(bundle.Sources)-1]
				bundle.Sources = bundle.Sources[:len(bundle.Sources)-1]
				addSkip(dropped.URI, "max_size budget exceeded")
				marshaled, merr = json.Marshal(bundle)
				if merr != nil {
					err = fmt.Errorf("get_context: remarshal json: %w", merr)
					return "", "", nil, err
				}
			}
		}
		out = string(marshaled)
	}

	return out, format, skipped, nil
}

// renderMarkdownMetadata formats a per-source metadata blockquote for the
// markdown-with-metadata context format. Returns an empty string when there
// is nothing to render so the content flows naturally.
func renderMarkdownMetadata(contentType string, tags []string) string {
	var lines []string
	if contentType != "" {
		lines = append(lines, fmt.Sprintf("> content-type: %s", contentType))
	}
	if len(tags) > 0 {
		lines = append(lines, fmt.Sprintf("> tags: %s", strings.Join(tags, ", ")))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n\n"
}

// ───────────────────── search ─────────────────────

// SearchResult wraps backend.SearchResult with the originating backend name
// for the response shape.
type SearchResult struct {
	backend.SearchResult
	Backend string `json:"backend"`
}

// Search runs a query across one or more backends and merges the results.
// backendNames is optional — if empty, every configured backend is queried.
// Filter checks are applied to each result.
func (d *ToolDispatcher) Search(ctx context.Context, query string, limit int, backendNames []string, caller model.Caller) ([]SearchResult, error) {
	start := time.Now()

	var out []SearchResult
	var rootErr error
	defer func() {
		d.auditLog.Log(audit.NewEntry(caller, "pf_scan",
			map[string]any{"query": query, "limit": limit, "backends": backendNames},
			start, len(out), rootErr))
	}()

	if query == "" {
		rootErr = fmt.Errorf("%w: empty query", model.ErrInvalidRequest)
		return nil, rootErr
	}
	if limit <= 0 {
		limit = 10
	}

	targets, err := d.resolveBackends(backendNames)
	if err != nil {
		rootErr = err
		return nil, err
	}

	perBackend := limit
	if len(targets) > 1 {
		// Give each backend a proportional slice, rounded up.
		perBackend = (limit + len(targets) - 1) / len(targets)
		if perBackend < 1 {
			perBackend = 1
		}
	}

	for _, b := range targets {
		if ctx.Err() != nil {
			break
		}
		results, serr := b.Search(ctx, query, perBackend)
		if serr != nil {
			// Continue — one backend failing shouldn't break search.
			continue
		}
		for _, r := range results {
			if !d.filter.AllowURI(r.URI, &caller) {
				continue
			}
			if !d.filter.AllowTags(r.URI, searchResultTags(r), &caller) {
				continue
			}
			out = append(out, SearchResult{SearchResult: r, Backend: b.Name()})
			if len(out) >= limit {
				break
			}
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// resolveBackends returns the list of backends to query. If names is empty
// every backend is returned in configured order.
func (d *ToolDispatcher) resolveBackends(names []string) ([]backend.Backend, error) {
	if len(names) == 0 {
		return d.backendsOrdered, nil
	}
	var out []backend.Backend
	for _, n := range names {
		b, ok := d.backends[n]
		if !ok {
			return nil, fmt.Errorf("%w: %q", model.ErrBackendNotFound, n)
		}
		out = append(out, b)
	}
	return out, nil
}

// ───────────────────── read ─────────────────────

// Read fetches a resource by URI. fromLine and toLine are optional (1-indexed,
// inclusive); zero means "no slicing for that bound". The backend is chosen
// from the URI's scheme.
func (d *ToolDispatcher) Read(ctx context.Context, uri string, fromLine, toLine int, caller model.Caller) (*backend.Resource, error) {
	start := time.Now()

	var res *backend.Resource
	var err error
	defer func() {
		size := 0
		if res != nil {
			size = len(res.Content)
		}
		d.auditLog.Log(audit.NewEntry(caller, "pf_peek",
			map[string]any{"uri": uri, "from_line": fromLine, "to_line": toLine},
			start, size, err))
	}()

	if !d.filter.AllowURI(uri, &caller) {
		err = fmt.Errorf("%w: blocked by filter", model.ErrAccessViolation)
		return nil, err
	}

	be, ferr := d.backendForURI(uri)
	if ferr != nil {
		err = ferr
		return nil, err
	}

	res, err = be.Read(ctx, uri)
	if err != nil {
		return nil, err
	}

	if !d.filter.AllowTags(res.URI, resourceTags(res), &caller) {
		res = nil
		err = fmt.Errorf("%w: blocked by tag filter", model.ErrAccessViolation)
		return nil, err
	}

	res.Content = d.filter.FilterContent(res.Content, res.URI)

	if fromLine > 0 || toLine > 0 {
		res.Content = backend.SliceLines(res.Content, fromLine, toLine)
	}

	return res, nil
}

// backendForURI finds the backend registered for the URI's scheme.
func (d *ToolDispatcher) backendForURI(uri string) (backend.Backend, error) {
	idx := strings.Index(uri, "://")
	if idx <= 0 {
		return nil, fmt.Errorf("%w: missing scheme in %q", model.ErrInvalidRequest, uri)
	}
	scheme := uri[:idx]
	b, ok := d.schemeToBackend[scheme]
	if !ok {
		return nil, fmt.Errorf("%w: no backend for scheme %q", model.ErrBackendNotFound, scheme)
	}
	return b, nil
}

// BackendForURI is the exported form of backendForURI. It lets the
// tool layer (specifically pf_poke's format:"raw" pre-flight) peek
// at the backend that owns a URI without duplicating scheme parsing.
// Callers that only need the generic Backend behaviour should stay on
// the internal method.
func (d *ToolDispatcher) BackendForURI(uri string) (backend.Backend, error) {
	return d.backendForURI(uri)
}

// ───────────────────── list_agents (pf_ps) ─────────────────────

// AgentSummary is the lightweight shape used by ListAgents. Each entry
// identifies one agent plus the backend that exposes it — clients need
// the backend name to disambiguate when two backends configure the same
// agent id (rare but possible).
type AgentSummary struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Backend     string `json:"backend"`
}

// ListAgents returns every agent exposed by every registered
// SubagentBackend, preserving backend-registration order.
func (d *ToolDispatcher) ListAgents(ctx context.Context, caller model.Caller) ([]AgentSummary, error) {
	start := time.Now()

	var out []AgentSummary
	for _, b := range d.backendsOrdered {
		sb, ok := b.(backend.SubagentBackend)
		if !ok {
			continue
		}
		for _, a := range sb.ListAgents() {
			out = append(out, AgentSummary{
				ID:          a.ID,
				Description: a.Description,
				Backend:     b.Name(),
			})
		}
	}
	d.auditLog.Log(audit.NewEntry(caller, "pf_ps", nil, start, len(out), nil))
	return out, nil
}

// ───────────────────── deep_retrieve (pf_fault) ─────────────────────

// DeepRetrieveResult is the structured response for a pf_fault call.
//
// Shape depends on whether the caller requested synchronous wait or
// async polling:
//
//   - Async (default, Wait=false): TaskID / Status / Agent / Backend
//     are populated; the terminal-only fields (Answer, Elapsed, …)
//     are zero. The caller polls GetTask (pf_ps with a task_id) until
//     Status is terminal.
//   - Sync (Wait=true): the call blocks until the task reaches a
//     terminal state and the result carries Status plus the
//     terminal-only fields (Answer on success, PartialResult+TimedOut
//     on timeout, Error on failure). TaskID is still populated so
//     clients can correlate with audit logs.
type DeepRetrieveResult struct {
	// TaskID is the pf_tk_* identifier for the underlying async
	// task. Always set — even for synchronous wait calls — so
	// clients can correlate to audit entries.
	TaskID string `json:"task_id"`
	// Status reflects the task lifecycle state at the time the
	// response was built. Terminal values are "done", "failed",
	// or "timed_out"; non-terminal is "running".
	Status string `json:"status"`
	// Agent is the resolved subagent id the task is running.
	Agent string `json:"agent"`
	// Backend is the subagent backend name the task is running on.
	Backend string `json:"backend"`
	// SpawnID is the pf_sp_* random token the dispatcher minted for
	// this call. Included in the response so callers can correlate
	// with downstream session logs (e.g. an openclaw gateway run).
	SpawnID string `json:"spawn_id,omitempty"`

	// Fields below are populated only when Status is terminal.

	// Answer is the subagent's final response on success.
	Answer string `json:"answer,omitempty"`
	// ElapsedSeconds is the wall-clock duration from task submit
	// to terminal state.
	ElapsedSeconds float64 `json:"elapsed_seconds,omitempty"`
	// TimedOut is true when Status is "timed_out".
	TimedOut bool `json:"timed_out,omitempty"`
	// PartialResult carries whatever the subagent produced before
	// a timeout fired. Empty for clean success/failure.
	PartialResult string `json:"partial_result,omitempty"`
	// Error is the stringified failure message when Status is
	// "failed".
	Error string `json:"error,omitempty"`
}

// DeepRetrieveOptions bundles the optional knobs for DeepRetrieve
// (time range, sync wait, future caller hints) so we can grow the
// call shape without churning every call site.
type DeepRetrieveOptions struct {
	// TimeRange is a free-form hint restricting the subagent's
	// search to a time window. Passed through to the backend's
	// prompt template as {time_range}; empty means "no restriction".
	TimeRange string
	// Wait toggles the sync compatibility path. When true,
	// DeepRetrieve blocks until the task reaches a terminal state
	// (or the caller context cancels) and returns the full result
	// inline. When false (the 0.10.0 default), the call returns
	// immediately with {task_id, status=running} and the caller
	// polls GetTask (pf_ps with a task_id) until it's done.
	Wait bool
}

// DeepRetrieve spawns a subagent to answer the query. In async mode
// (the default, Wait=false) it submits a task, audits the start, and
// returns immediately with the task id. In sync mode (Wait=true) it
// blocks on task completion and returns the full terminal result.
//
// agentID may be empty (use the first subagent backend's default).
// timeout overrides the backend's configured default when non-zero.
// opts carries TimeRange and Wait toggles.
//
// On timeout, the result carries TimedOut=true and PartialResult for
// synchronous callers. Unknown agent / no-subagent errors propagate
// via the error return *before* a task is submitted.
func (d *ToolDispatcher) DeepRetrieve(ctx context.Context, query string, agentID string, timeout time.Duration, caller model.Caller, opts DeepRetrieveOptions) (*DeepRetrieveResult, error) {
	start := time.Now()

	var result *DeepRetrieveResult
	var err error
	defer func() {
		size := 0
		if result != nil {
			size = len(result.Answer) + len(result.PartialResult)
		}
		args := map[string]any{
			"query":     query,
			"agent":     agentID,
			"timeout_s": int(timeout.Seconds()),
			"wait":      opts.Wait,
		}
		if opts.TimeRange != "" {
			args["time_range"] = opts.TimeRange
		}
		if result != nil {
			args["task_id"] = result.TaskID
			args["spawn_id"] = result.SpawnID
			args["status"] = result.Status
		}
		d.auditLog.Log(audit.NewEntry(caller, "pf_fault", args, start, size, err))
	}()

	if query == "" {
		err = fmt.Errorf("%w: empty query", model.ErrInvalidRequest)
		return nil, err
	}

	target, agentName, ferr := d.findSubagent(agentID)
	if ferr != nil {
		err = ferr
		return nil, err
	}

	spawnID, err := task.GenerateSpawnID()
	if err != nil {
		return nil, fmt.Errorf("dispatcher: generate spawn id: %w", err)
	}

	// Submit the task to the in-memory manager. Run() wraps
	// target.Spawn — it runs on the manager's detached context so
	// HTTP disconnects do not kill the subagent.
	submitted, submitErr := d.tasks.Submit(task.SubmitRequest{
		Agent:    agentName,
		Backend:  target.Name(),
		CallerID: caller.ID,
		SpawnID:  spawnID,
		Query:    query,
		Timeout:  timeout,
		Run: func(runCtx context.Context) (string, error) {
			answer, spawnErr := target.Spawn(runCtx, backend.SpawnRequest{
				AgentID:   agentName,
				Task:      query,
				Purpose:   backend.SpawnPurposeRetrieve,
				TimeRange: opts.TimeRange,
				SpawnID:   spawnID,
				Timeout:   timeout,
			})
			if errors.Is(spawnErr, model.ErrSubagentTimeout) {
				slog.Warn("deep_retrieve: subagent timed out",
					"agent", agentName, "backend", target.Name(),
					"caller", caller.ID, "task_id", "", "spawn_id", spawnID)
				return "", &task.TimeoutError{Partial: answer}
			}
			return answer, spawnErr
		},
	})
	if submitErr != nil {
		if errors.Is(submitErr, task.ErrBackpressure) {
			err = fmt.Errorf("%w: task manager at max_concurrent", model.ErrRateLimited)
			return nil, err
		}
		err = submitErr
		return nil, err
	}

	if !opts.Wait {
		// Async path — return the running snapshot immediately.
		result = taskToResult(submitted)
		return result, nil
	}

	// Sync wait path — block on the task until it reaches a
	// terminal state, the caller ctx fires, or the task manager
	// closes. If the caller ctx fires, surface whatever snapshot
	// is currently available so the client can correlate the
	// task_id and keep polling.
	final, waitErr := d.tasks.Wait(ctx, submitted.ID)
	if waitErr != nil {
		if errors.Is(waitErr, context.DeadlineExceeded) || errors.Is(waitErr, context.Canceled) {
			if current, gerr := d.tasks.Get(submitted.ID); gerr == nil {
				result = taskToResult(current)
				return result, nil
			}
		}
		err = waitErr
		return nil, err
	}
	result = taskToResult(final)
	return result, nil
}

// GetTask returns the current snapshot of a task as a
// DeepRetrieveResult. Used by the pf_ps poll path — the output
// shape is identical to DeepRetrieve's async return so clients can
// pass either one through the same decoder.
func (d *ToolDispatcher) GetTask(ctx context.Context, taskID string, caller model.Caller) (*DeepRetrieveResult, error) {
	start := time.Now()

	var result *DeepRetrieveResult
	var err error
	defer func() {
		args := map[string]any{"task_id": taskID}
		if result != nil {
			args["status"] = result.Status
		}
		d.auditLog.Log(audit.NewEntry(caller, "pf_ps", args, start, 0, err))
	}()

	t, getErr := d.tasks.Get(taskID)
	if getErr != nil {
		if errors.Is(getErr, task.ErrTaskNotFound) {
			err = fmt.Errorf("%w: task %q", model.ErrResourceNotFound, taskID)
			return nil, err
		}
		err = getErr
		return nil, err
	}
	result = taskToResult(t)
	return result, nil
}

// TaskManager exposes the dispatcher's task manager so the server's
// shutdown path can Close it. Exported for lifecycle integration,
// not for routine use.
func (d *ToolDispatcher) TaskManager() *task.Manager { return d.tasks }

// taskToResult translates a task.Task snapshot into the dispatcher's
// DeepRetrieveResult shape. Shared by DeepRetrieve (async + wait
// paths), DelegateWrite, and GetTask.
func taskToResult(t *task.Task) *DeepRetrieveResult {
	r := &DeepRetrieveResult{
		TaskID:  t.ID,
		Status:  string(t.Status),
		Agent:   t.Agent,
		Backend: t.Backend,
		SpawnID: t.SpawnID,
	}
	switch t.Status {
	case task.StatusDone:
		r.Answer = t.Result
		r.ElapsedSeconds = t.Elapsed
	case task.StatusTimedOut:
		r.TimedOut = true
		r.PartialResult = t.Result
		r.ElapsedSeconds = t.Elapsed
	case task.StatusFailed:
		r.Error = t.Error
		r.ElapsedSeconds = t.Elapsed
	}
	return r
}

// DelegateWriteOptions carries the optional knobs for DelegateWrite —
// the free-form Target hint that tells the subagent where to prefer
// persisting the content, plus the Wait toggle that matches
// DeepRetrieveOptions so pf_poke mode:agent keeps parity with pf_fault.
type DelegateWriteOptions struct {
	// Target is a free-form placement hint passed through to the
	// subagent via the prompt template's {target} placeholder
	// ("daily", "long-term", "auto", etc.). Empty defaults to
	// "auto" at the handler layer.
	Target string
	// Wait toggles the sync compatibility path. See
	// DeepRetrieveOptions.Wait for the semantics; pf_poke
	// mode:agent defaults to Wait=true because write calls are
	// typically expected to confirm placement before returning.
	Wait bool
}

// DelegateWrite spawns a subagent and asks it to persist `content`
// into the user's memory. It is the dispatcher entry point for
// pf_poke mode:"agent". Structurally identical to DeepRetrieve —
// generate a spawn_id, submit a task, wait or return the async
// handle — but with SpawnPurposeWrite so the backend picks the
// write-framed prompt template instead of the retrieval-framed one.
//
// The audit entry is emitted under tool: "pf_fault" to match the
// 0.5.x contract documented in docs/security.md §Audit (agent-mode
// writes currently share the pf_fault row because they are, in the
// end, subagent spawns — revisit in Phase 5 when the structured
// subagent response format lands).
func (d *ToolDispatcher) DelegateWrite(ctx context.Context, content string, agentID string, timeout time.Duration, caller model.Caller, opts DelegateWriteOptions) (*DeepRetrieveResult, error) {
	start := time.Now()

	var result *DeepRetrieveResult
	var err error
	defer func() {
		size := 0
		if result != nil {
			size = len(result.Answer) + len(result.PartialResult)
		}
		args := map[string]any{
			"content_bytes": len(content),
			"agent":         agentID,
			"timeout_s":     int(timeout.Seconds()),
			"purpose":       "write",
			"wait":          opts.Wait,
		}
		if opts.Target != "" {
			args["target"] = opts.Target
		}
		if result != nil {
			args["task_id"] = result.TaskID
			args["spawn_id"] = result.SpawnID
			args["status"] = result.Status
		}
		d.auditLog.Log(audit.NewEntry(caller, "pf_fault", args, start, size, err))
	}()

	if content == "" {
		err = fmt.Errorf("%w: empty content", model.ErrInvalidRequest)
		return nil, err
	}

	target, agentName, ferr := d.findSubagent(agentID)
	if ferr != nil {
		err = ferr
		return nil, err
	}

	spawnID, err := task.GenerateSpawnID()
	if err != nil {
		return nil, fmt.Errorf("dispatcher: generate spawn id: %w", err)
	}

	submitted, submitErr := d.tasks.Submit(task.SubmitRequest{
		Agent:    agentName,
		Backend:  target.Name(),
		CallerID: caller.ID,
		SpawnID:  spawnID,
		Query:    content,
		Timeout:  timeout,
		Run: func(runCtx context.Context) (string, error) {
			answer, spawnErr := target.Spawn(runCtx, backend.SpawnRequest{
				AgentID: agentName,
				Task:    content,
				Purpose: backend.SpawnPurposeWrite,
				Target:  opts.Target,
				SpawnID: spawnID,
				Timeout: timeout,
			})
			if errors.Is(spawnErr, model.ErrSubagentTimeout) {
				slog.Warn("delegate_write: subagent timed out",
					"agent", agentName, "backend", target.Name(),
					"caller", caller.ID, "spawn_id", spawnID)
				return "", &task.TimeoutError{Partial: answer}
			}
			return answer, spawnErr
		},
	})
	if submitErr != nil {
		if errors.Is(submitErr, task.ErrBackpressure) {
			err = fmt.Errorf("%w: task manager at max_concurrent", model.ErrRateLimited)
			return nil, err
		}
		err = submitErr
		return nil, err
	}

	if !opts.Wait {
		result = taskToResult(submitted)
		return result, nil
	}

	final, waitErr := d.tasks.Wait(ctx, submitted.ID)
	if waitErr != nil {
		if errors.Is(waitErr, context.DeadlineExceeded) || errors.Is(waitErr, context.Canceled) {
			if current, gerr := d.tasks.Get(submitted.ID); gerr == nil {
				result = taskToResult(current)
				return result, nil
			}
		}
		err = waitErr
		return nil, err
	}
	result = taskToResult(final)
	return result, nil
}

// findSubagent locates a SubagentBackend that exposes the requested
// agent id. An empty id returns the first subagent backend's default
// agent. If no SubagentBackend is configured at all, ErrAgentNotFound is
// returned with a descriptive message.
func (d *ToolDispatcher) findSubagent(agentID string) (backend.SubagentBackend, string, error) {
	var firstSub backend.SubagentBackend
	for _, b := range d.backendsOrdered {
		sb, ok := b.(backend.SubagentBackend)
		if !ok {
			continue
		}
		if firstSub == nil {
			firstSub = sb
		}
		if agentID == "" {
			continue
		}
		for _, a := range sb.ListAgents() {
			if a.ID == agentID {
				return sb, agentID, nil
			}
		}
	}

	if firstSub == nil {
		return nil, "", fmt.Errorf("%w: no subagent backend configured", model.ErrAgentNotFound)
	}
	if agentID != "" {
		return nil, "", fmt.Errorf("%w: %q", model.ErrAgentNotFound, agentID)
	}
	// Empty id → pick the first configured agent of the first subagent
	// backend. Every SubagentBackend constructor guarantees at least one
	// agent.
	agents := firstSub.ListAgents()
	if len(agents) == 0 {
		return nil, "", fmt.Errorf("%w: backend %q has no agents", model.ErrAgentNotFound, firstSub.Name())
	}
	return firstSub, agents[0].ID, nil
}

// ───────────────────── write (pf_poke) ─────────────────────

// WriteResult is the structured response from Write. It carries only
// the fields pf_poke needs to echo back to the caller — the raw bytes
// are never part of the response to keep the audit log slim.
type WriteResult struct {
	URI          string `json:"uri"`
	BytesWritten int    `json:"bytes_written"`
	Backend      string `json:"backend"`
}

// Write mutates a resource by URI. The content passed in is the final
// bytes the caller wants appended — any entry-template wrapping must
// already be applied by the tool layer. The dispatcher is responsible
// for:
//
//  1. Resolving the backend that owns the URI's scheme.
//  2. Asserting the backend implements [backend.WritableBackend] and
//     is actually writable (a non-writable backend fails with
//     [model.ErrAccessViolation]).
//  3. Running the filter pipeline's AllowWriteURI check (the
//     server-wide Phase-4 write allowlist).
//  4. Delegating to the backend's Write method, which enforces the
//     per-backend `write_paths` allowlist, write_mode rules, and
//     max_entry_size cap.
//  5. Emitting an audit entry with tool="pf_poke", sanitized args,
//     and the bytes-written count (never the content body itself).
func (d *ToolDispatcher) Write(ctx context.Context, uri string, content string, caller model.Caller) (*WriteResult, error) {
	start := time.Now()

	var result *WriteResult
	var err error
	defer func() {
		size := 0
		if result != nil {
			size = result.BytesWritten
		}
		d.auditLog.Log(audit.NewEntry(caller, "pf_poke",
			map[string]any{"uri": uri, "bytes": len(content)},
			start, size, err))
	}()

	if uri == "" {
		err = fmt.Errorf("%w: uri is required", model.ErrInvalidRequest)
		return nil, err
	}

	// Server-wide write allowlist (filters.path.write_allow/write_deny).
	if !d.filter.AllowWriteURI(uri, &caller) {
		err = fmt.Errorf("%w: blocked by write filter", model.ErrAccessViolation)
		return nil, err
	}

	be, ferr := d.backendForURI(uri)
	if ferr != nil {
		err = ferr
		return nil, err
	}
	wb, ok := be.(backend.WritableBackend)
	if !ok || !wb.Writable() {
		err = fmt.Errorf("%w: backend %q is read-only", model.ErrAccessViolation, be.Name())
		return nil, err
	}

	n, werr := wb.Write(ctx, uri, content)
	if werr != nil {
		err = werr
		return nil, err
	}
	result = &WriteResult{
		URI:          uri,
		BytesWritten: n,
		Backend:      be.Name(),
	}
	return result, nil
}

// Close releases dispatcher-owned resources: the task manager (which
// cancels every in-flight task and waits for the goroutines to
// return) and then the audit logger. Close is idempotent.
func (d *ToolDispatcher) Close() error {
	if d.tasks != nil {
		_ = d.tasks.Close()
	}
	if d.auditLog != nil {
		return d.auditLog.Close()
	}
	return nil
}

// ───────────────────── helpers ─────────────────────

// resourceTags extracts the "tags" metadata field from a resource, if any.
func resourceTags(r *backend.Resource) []string {
	if r == nil || r.Metadata == nil {
		return nil
	}
	return anyToStringSlice(r.Metadata["tags"])
}

// searchResultTags extracts tags from a search result's metadata.
func searchResultTags(r backend.SearchResult) []string {
	if r.Metadata == nil {
		return nil
	}
	return anyToStringSlice(r.Metadata["tags"])
}

// anyToStringSlice converts an any value into a []string, accepting both
// []string and []any with string elements.
func anyToStringSlice(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// SortedBackendNames returns the list of backend names in deterministic
// order. Useful for health checks and diagnostics.
func (d *ToolDispatcher) SortedBackendNames() []string {
	names := make([]string, 0, len(d.backends))
	for n := range d.backends {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
