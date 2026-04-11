package tool

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/model"
	"github.com/jet/pagefault/internal/write"
)

// WriteInput is the request shape for pf_poke.
//
// Mode selects between the two write strategies:
//
//   - "direct" — pagefault appends the content to URI directly. Fast,
//     deterministic, zero-token. URI, Content, and Format apply.
//   - "agent"  — pagefault spawns a subagent and hands it the task of
//     deciding *where* and *how* to write. Content, Agent, Target,
//     and TimeoutSeconds apply.
//
// Unknown fields are ignored; unknown Modes return ErrInvalidRequest.
type WriteInput struct {
	URI            string `json:"uri,omitempty"`
	Content        string `json:"content"`
	Mode           string `json:"mode"`
	Format         string `json:"format,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Target         string `json:"target,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// WriteOutput is the response shape for pf_poke. The direct and agent
// modes share the envelope — fields that don't apply to the chosen
// mode are omitted via `omitempty`.
type WriteOutput struct {
	Status         string   `json:"status"`
	Mode           string   `json:"mode"`
	URI            string   `json:"uri,omitempty"`
	BytesWritten   int      `json:"bytes_written,omitempty"`
	Format         string   `json:"format,omitempty"`
	Backend        string   `json:"backend,omitempty"`
	Agent          string   `json:"agent,omitempty"`
	ElapsedSeconds float64  `json:"elapsed_seconds,omitempty"`
	Result         string   `json:"result,omitempty"`
	TargetsWritten []string `json:"targets_written,omitempty"`
	TimedOut       bool     `json:"timed_out,omitempty"`
}

// writeClock is swapped by tests so the entry template is
// deterministic. Production code uses [write.DefaultClock].
var writeClock write.Clock = write.DefaultClock

// HandleWrite is the pure, transport-agnostic body of pf_poke. It
// routes the request to one of two strategies:
//
//   - [handleWriteDirect] for mode:"direct" (filesystem append)
//   - [handleWriteAgent]  for mode:"agent"  (subagent writeback)
//
// Input validation common to both modes (empty content, unknown
// mode) happens here. Mode-specific validation and the actual work
// live in the helpers.
func HandleWrite(ctx context.Context, d *dispatcher.ToolDispatcher, in WriteInput, caller model.Caller) (WriteOutput, error) {
	if in.Content == "" {
		return WriteOutput{}, fmt.Errorf("%w: content is required", model.ErrInvalidRequest)
	}
	switch in.Mode {
	case "direct":
		return handleWriteDirect(ctx, d, in, caller)
	case "agent":
		return handleWriteAgent(ctx, d, in, caller)
	case "":
		return WriteOutput{}, fmt.Errorf("%w: mode is required (\"direct\" or \"agent\")", model.ErrInvalidRequest)
	default:
		return WriteOutput{}, fmt.Errorf("%w: unknown mode %q (expected \"direct\" or \"agent\")", model.ErrInvalidRequest, in.Mode)
	}
}

// handleWriteDirect implements mode:"direct" — format the content
// with [write.FormatEntry], then call the dispatcher's Write method
// (which runs the filter pipeline, routes to the backend, and
// enforces backend-level write policy).
//
// Size enforcement happens *here*, against the raw caller content,
// before FormatEntry adds its ~40–60 byte header. This keeps the
// budget "raw and entry share the same limit", as promised by the
// [model.ErrContentTooLarge] docstring and docs/security.md §Write
// safety. Moving it to the backend would penalise format:"entry" by
// the wrapper overhead.
func handleWriteDirect(ctx context.Context, d *dispatcher.ToolDispatcher, in WriteInput, caller model.Caller) (WriteOutput, error) {
	if in.URI == "" {
		return WriteOutput{}, fmt.Errorf("%w: uri is required for mode:direct", model.ErrInvalidRequest)
	}
	format := write.EntryFormat(in.Format)
	if format == "" {
		format = write.EntryFormatEntry
	}

	// Peek the backend once for raw-size and format:"raw" pre-flight
	// checks. A backend error here (not writable, unknown scheme)
	// short-circuits the whole call with a clean sentinel.
	be, err := backendForDirectWrite(d, in.URI)
	if err != nil {
		return WriteOutput{}, err
	}
	if max := be.MaxEntrySize(); max > 0 && len(in.Content) > max {
		return WriteOutput{}, fmt.Errorf("%w: %d bytes exceeds max_entry_size %d",
			model.ErrContentTooLarge, len(in.Content), max)
	}
	// "raw" is a second-tier opt-in: the backend must also be in
	// write_mode:"any". The dispatcher can't check this without
	// inspecting the backend, and the error site is clearer here.
	if format == write.EntryFormatRaw && be.WriteMode() != "any" {
		return WriteOutput{}, fmt.Errorf("%w: format:\"raw\" requires the backend to be write_mode:\"any\" (got %q)", model.ErrInvalidRequest, be.WriteMode())
	}

	body, ferr := write.FormatEntry(in.Content, format, callerLabelFor(caller), writeClock)
	if ferr != nil {
		return WriteOutput{}, fmt.Errorf("%w: %s", model.ErrInvalidRequest, ferr.Error())
	}

	res, err := d.Write(ctx, in.URI, body, caller)
	if err != nil {
		return WriteOutput{}, err
	}
	return WriteOutput{
		Status:       "written",
		Mode:         "direct",
		URI:          res.URI,
		BytesWritten: res.BytesWritten,
		Format:       string(format),
		Backend:      res.Backend,
	}, nil
}

// handleWriteAgent implements mode:"agent" — hand the content to a
// subagent via the dispatcher's DelegateWrite path. The subagent has
// its own tools and workspace access, and gets a write-framed prompt
// template (per-agent override → backend default →
// [backend.DefaultWritePromptTemplate]) applied inside Spawn so it
// knows its job is placement, not generation. pagefault does not
// re-validate the agent's writes — agent mode *delegates trust* to
// the subagent. See docs/security.md → "Mode: agent" for the full
// trust-boundary rationale.
func handleWriteAgent(ctx context.Context, d *dispatcher.ToolDispatcher, in WriteInput, caller model.Caller) (WriteOutput, error) {
	target := in.Target
	if target == "" {
		target = "auto"
	}
	timeout := time.Duration(in.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultDeepRetrieveTimeout
	}

	// pf_poke mode:agent is synchronous-by-default — clients expect
	// the call to return with placement confirmed, not with a bare
	// task id to poll. The 0.10.0 async task manager still runs the
	// Spawn on a detached goroutine so HTTP disconnects do not kill
	// the subagent, but the dispatcher blocks on task completion
	// and surfaces the final result inline.
	res, err := d.DelegateWrite(ctx, in.Content, in.Agent, timeout, caller, dispatcher.DelegateWriteOptions{
		Target: target,
		Wait:   true,
	})
	if err != nil {
		return WriteOutput{}, err
	}

	answer := res.Answer
	if res.TimedOut {
		// Preserve whatever the subagent produced before the
		// deadline; surface the timeout as a success envelope so
		// clients can inspect the partial text rather than branching
		// on an error code.
		answer = res.PartialResult
	}

	return WriteOutput{
		Status:         "written",
		Mode:           "agent",
		Agent:          res.Agent,
		Backend:        res.Backend,
		ElapsedSeconds: res.ElapsedSeconds,
		Result:         answer,
		TimedOut:       res.TimedOut,
	}, nil
}

// backendForDirectWrite resolves the backend that owns the URI scheme
// and returns it as a WritableBackend if the type assertion passes.
// Returns wrapped sentinel errors so the caller can propagate them
// through the REST envelope with the right code/status.
//
// It's a thin helper around dispatcher internals; pf_poke uses it to
// peek at the backend's write_mode config before calling Write,
// specifically to reject format:"raw" on append-only backends with a
// clear message instead of a generic access-violation.
func backendForDirectWrite(d *dispatcher.ToolDispatcher, uri string) (writableBackendAccessor, error) {
	be, err := d.BackendForURI(uri)
	if err != nil {
		return nil, err
	}
	wb, ok := be.(writableBackendAccessor)
	if !ok {
		return nil, fmt.Errorf("%w: backend %q is read-only", model.ErrAccessViolation, be.Name())
	}
	if !wb.Writable() {
		return nil, fmt.Errorf("%w: backend %q is read-only", model.ErrAccessViolation, be.Name())
	}
	return wb, nil
}

// writableBackendAccessor is the minimal subset of
// [backend.WritableBackend] that HandleWrite needs for pre-flight
// checks. Kept as an internal interface so the tool package doesn't
// import the backend package just for a type assertion.
//
// MaxEntrySize is used by handleWriteDirect to cap the *raw* caller
// content before entry-template wrapping (see [model.ErrContentTooLarge]
// docstring) — doing the check here rather than in the backend keeps
// format:"entry" and format:"raw" on the same budget, as documented.
type writableBackendAccessor interface {
	Name() string
	Writable() bool
	WriteMode() string
	MaxEntrySize() int
}

// callerLabelFor returns the human-readable caller label used in the
// entry-template header. Falls back to the id if no label is set.
func callerLabelFor(c model.Caller) string {
	if c.Label != "" {
		return c.Label
	}
	return c.ID
}

// Static check: HandleWrite must not accidentally stop returning
// ErrInvalidRequest for its validation failures — that would scramble
// the REST envelope's error code. The test suite asserts this, but we
// keep a compile-time reference to the sentinel here so the import
// chain stays explicit.
var _ = errors.Is(model.ErrInvalidRequest, model.ErrInvalidRequest)
