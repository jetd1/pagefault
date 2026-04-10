// Package dispatcher is the central tool router for pagefault.
//
// The dispatcher holds the registered backends, named contexts, filter
// pipeline, and audit logger. Tool handlers call dispatcher methods to
// perform the actual work — the dispatcher is where filtering and auditing
// happen uniformly for every tool.
package dispatcher

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jet/pagefault/internal/audit"
	"github.com/jet/pagefault/internal/backend"
	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/filter"
	"github.com/jet/pagefault/internal/model"
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
}

// Options bundles the dependencies required to construct a ToolDispatcher.
type Options struct {
	Backends []backend.Backend
	Contexts []config.ContextConfig
	Filter   *filter.CompositeFilter
	Audit    audit.Logger
	Tools    config.ToolsConfig
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

	d := &ToolDispatcher{
		backends:        make(map[string]backend.Backend, len(opts.Backends)),
		schemeToBackend: make(map[string]backend.Backend),
		contexts:        make(map[string]config.ContextConfig, len(opts.Contexts)),
		contextsOrdered: append([]config.ContextConfig(nil), opts.Contexts...),
		filter:          opts.Filter,
		auditLog:        opts.Audit,
		toolsCfg:        opts.Tools,
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
	d.auditLog.Log(audit.NewEntry(caller, "list_contexts", nil, start, len(out), nil))
	return out, nil
}

// ───────────────────── get_context ─────────────────────

// GetContext loads the named context, concatenates its sources (applying
// filters), and truncates to max_size if necessary.
//
// format overrides the context's configured format when non-empty. Phase 1
// supports "markdown" (concatenate with separators).
func (d *ToolDispatcher) GetContext(ctx context.Context, name string, format string, caller model.Caller) (string, error) {
	start := time.Now()

	var out string
	var err error
	defer func() {
		d.auditLog.Log(audit.NewEntry(caller, "get_context",
			map[string]any{"name": name, "format": format},
			start, len(out), err))
	}()

	cfg, ok := d.contexts[name]
	if !ok {
		err = fmt.Errorf("%w: %q", model.ErrContextNotFound, name)
		return "", err
	}

	if format == "" {
		format = cfg.Format
	}
	if format == "" {
		format = "markdown"
	}

	var parts []string
	for _, src := range cfg.Sources {
		be, ok := d.backends[src.Backend]
		if !ok {
			err = fmt.Errorf("%w: context %q references unknown backend %q", model.ErrBackendNotFound, name, src.Backend)
			return "", err
		}
		if !d.filter.AllowURI(src.URI, &caller) {
			continue // filtered out — skip silently
		}
		res, rerr := be.Read(ctx, src.URI)
		if rerr != nil {
			// Skip individual source errors rather than failing the whole
			// context; a missing file shouldn't break the whole bundle.
			continue
		}
		if !d.filter.AllowTags(res.URI, resourceTags(res), &caller) {
			continue
		}
		content := d.filter.FilterContent(res.Content, res.URI)
		parts = append(parts, fmt.Sprintf("# %s\n\n%s", res.URI, content))
	}

	joined := strings.Join(parts, "\n\n---\n\n")
	if cfg.MaxSize > 0 && len(joined) > cfg.MaxSize {
		joined = joined[:cfg.MaxSize] + "\n\n...[truncated]"
	}
	out = joined
	return out, nil
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
		d.auditLog.Log(audit.NewEntry(caller, "search",
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
		d.auditLog.Log(audit.NewEntry(caller, "read",
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

// Close releases dispatcher-owned resources (primarily the audit logger).
func (d *ToolDispatcher) Close() error {
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
