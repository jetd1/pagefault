package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/audit"
	"github.com/jet/pagefault/internal/backend"
	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/filter"
	"github.com/jet/pagefault/internal/model"
	"github.com/jet/pagefault/internal/write"
)

// newWritableDispatcher returns a dispatcher with a single writable
// filesystem backend rooted at a tempdir.
func newWritableDispatcher(t *testing.T) (*dispatcher.ToolDispatcher, string) {
	t.Helper()
	root := t.TempDir()
	fsCfg := &config.FilesystemBackendConfig{
		Name:         "fs",
		Type:         "filesystem",
		Root:         root,
		Include:      []string{"**/*.md"},
		URIScheme:    "memory",
		Sandbox:      true,
		Writable:     true,
		WritePaths:   []string{"memory://notes/*.md"},
		WriteMode:    "append",
		MaxEntrySize: 200,
		FileLocking:  "flock",
	}
	be, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{be},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)
	return d, root
}

func TestHandleWrite_DirectHappyPath(t *testing.T) {
	// Pin the clock so the entry header is deterministic.
	prev := writeClock
	writeClock = func() time.Time { return time.Date(2026, 4, 11, 12, 34, 0, 0, time.UTC) }
	defer func() { writeClock = prev }()

	d, root := newWritableDispatcher(t)
	in := WriteInput{
		URI:     "memory://notes/today.md",
		Content: "first entry",
		Mode:    "direct",
	}
	caller := model.Caller{ID: "cli", Label: "pagefault CLI"}
	out, err := HandleWrite(context.Background(), d, in, caller)
	require.NoError(t, err)
	assert.Equal(t, "written", out.Status)
	assert.Equal(t, "direct", out.Mode)
	assert.Equal(t, "memory://notes/today.md", out.URI)
	assert.Equal(t, "entry", out.Format)
	assert.Equal(t, "fs", out.Backend)
	assert.Positive(t, out.BytesWritten)

	// File contents should contain the formatted entry.
	got, err := os.ReadFile(filepath.Join(root, "notes/today.md"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "## [12:34] via pagefault (pagefault CLI)")
	assert.Contains(t, string(got), "first entry")
}

func TestHandleWrite_DirectRawRequiresAnyMode(t *testing.T) {
	d, _ := newWritableDispatcher(t)
	in := WriteInput{
		URI:     "memory://notes/today.md",
		Content: "<raw>",
		Mode:    "direct",
		Format:  "raw",
	}
	_, err := HandleWrite(context.Background(), d, in, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
	assert.Contains(t, err.Error(), "write_mode:\"any\"")
}

func TestHandleWrite_DirectRawOnAnyModeBackend(t *testing.T) {
	root := t.TempDir()
	fsCfg := &config.FilesystemBackendConfig{
		Name:       "fs",
		Type:       "filesystem",
		Root:       root,
		Include:    []string{"**/*.md"},
		URIScheme:  "memory",
		Sandbox:    true,
		Writable:   true,
		WritePaths: []string{"memory://notes/*.md"},
		WriteMode:  "any",
	}
	be, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{be},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	out, err := HandleWrite(context.Background(), d, WriteInput{
		URI:     "memory://notes/raw.md",
		Content: "raw bytes only",
		Mode:    "direct",
		Format:  "raw",
	}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "raw", out.Format)

	got, err := os.ReadFile(filepath.Join(root, "notes/raw.md"))
	require.NoError(t, err)
	assert.Equal(t, "raw bytes only", string(got))
}

func TestHandleWrite_DirectMissingURI(t *testing.T) {
	d, _ := newWritableDispatcher(t)
	_, err := HandleWrite(context.Background(), d, WriteInput{
		Content: "x",
		Mode:    "direct",
	}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestHandleWrite_EmptyContent(t *testing.T) {
	d, _ := newWritableDispatcher(t)
	_, err := HandleWrite(context.Background(), d, WriteInput{
		URI:  "memory://notes/x.md",
		Mode: "direct",
	}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestHandleWrite_MissingMode(t *testing.T) {
	d, _ := newWritableDispatcher(t)
	_, err := HandleWrite(context.Background(), d, WriteInput{
		URI:     "memory://notes/x.md",
		Content: "x",
	}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestHandleWrite_UnknownMode(t *testing.T) {
	d, _ := newWritableDispatcher(t)
	_, err := HandleWrite(context.Background(), d, WriteInput{
		URI:     "memory://notes/x.md",
		Content: "x",
		Mode:    "spooky",
	}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
	assert.Contains(t, err.Error(), "unknown mode")
}

func TestHandleWrite_DirectBackendNotWritable(t *testing.T) {
	// Read-only filesystem backend — the dispatcher should reject
	// the write with ErrAccessViolation.
	root := t.TempDir()
	fsCfg := &config.FilesystemBackendConfig{
		Name:      "fs",
		Type:      "filesystem",
		Root:      root,
		Include:   []string{"**/*.md"},
		URIScheme: "memory",
		Sandbox:   true,
	}
	be, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{be},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	_, err = HandleWrite(context.Background(), d, WriteInput{
		URI:     "memory://notes/x.md",
		Content: "x",
		Mode:    "direct",
	}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAccessViolation))
}

func TestHandleWrite_DirectBlockedByWriteFilter(t *testing.T) {
	// Writable backend, but the server-wide write filter denies the URI.
	root := t.TempDir()
	fsCfg := &config.FilesystemBackendConfig{
		Name:         "fs",
		Type:         "filesystem",
		Root:         root,
		Include:      []string{"**/*.md"},
		URIScheme:    "memory",
		Sandbox:      true,
		Writable:     true,
		WritePaths:   []string{"memory://**/*.md"}, // backend permissive
		WriteMode:    "append",
		MaxEntrySize: 200,
	}
	be, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)

	// Path filter with a write-specific allowlist excluding our URI.
	pf, err := filter.NewPathFilter(nil, nil, []string{"memory://memory/*.md"}, nil)
	require.NoError(t, err)
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{be},
		Filter:   filter.NewCompositeFilter(pf),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	_, err = HandleWrite(context.Background(), d, WriteInput{
		URI:     "memory://notes/x.md",
		Content: "x",
		Mode:    "direct",
	}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAccessViolation))
}

func TestHandleWrite_DirectContentTooLarge(t *testing.T) {
	root := t.TempDir()
	fsCfg := &config.FilesystemBackendConfig{
		Name:         "fs",
		Type:         "filesystem",
		Root:         root,
		Include:      []string{"**/*.md"},
		URIScheme:    "memory",
		Sandbox:      true,
		Writable:     true,
		WritePaths:   []string{"memory://notes/*.md"},
		WriteMode:    "append",
		MaxEntrySize: 10,
	}
	be, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{be},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	// Raw content of 11 bytes against a 10-byte cap — the tool
	// layer rejects it before wrapping. (Regression guard: before
	// 0.5.1 the cap was enforced in the backend against the
	// *wrapped* bytes, so a 5-byte raw would fail after wrapping —
	// penalising format:"entry" by the wrapper overhead.)
	_, err = HandleWrite(context.Background(), d, WriteInput{
		URI:     "memory://notes/small.md",
		Content: "12345678901",
		Mode:    "direct",
	}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrContentTooLarge))
}

func TestHandleWrite_DirectContentAtCapSucceeds(t *testing.T) {
	// Regression guard for the 0.5.1 budget fix. Before the fix a
	// 10-byte raw input into a 10-byte cap would fail because the
	// backend checked the post-wrap bytes (≈60). After the fix the
	// raw content at the cap passes — format:"entry" and
	// format:"raw" share the same budget.
	root := t.TempDir()
	fsCfg := &config.FilesystemBackendConfig{
		Name:         "fs",
		Type:         "filesystem",
		Root:         root,
		Include:      []string{"**/*.md"},
		URIScheme:    "memory",
		Sandbox:      true,
		Writable:     true,
		WritePaths:   []string{"memory://notes/*.md"},
		WriteMode:    "append",
		MaxEntrySize: 10,
	}
	be, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{be},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	out, err := HandleWrite(context.Background(), d, WriteInput{
		URI:     "memory://notes/exact.md",
		Content: "1234567890", // exactly 10 bytes
		Mode:    "direct",
	}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "written", out.Status)
	// bytes_written is the on-disk bytes (wrapped) so it will
	// exceed the 10-byte cap — that's the entire point of the fix.
	assert.Greater(t, out.BytesWritten, 10)
}

// ───────────── agent mode ─────────────

// agentStub is a subagent backend used by the HandleWrite agent-mode
// tests. It records the full SpawnRequest so tests can assert the
// purpose, target, and raw task (no template wrapping — the stub
// bypasses the CLI/HTTP backend implementations that apply it).
//
// If `block` is set, Spawn waits on it (or on ctx.Done) before
// returning — used by the 0.10.1 caller-cancel regression test to
// hold the task in StatusRunning until the test's caller ctx fires.
type agentStub struct {
	name    string
	agents  []backend.AgentInfo
	answer  string
	err     error
	block   chan struct{}
	lastReq backend.SpawnRequest
}

func (s *agentStub) Name() string { return s.name }
func (s *agentStub) Read(context.Context, string) (*backend.Resource, error) {
	return nil, model.ErrResourceNotFound
}
func (s *agentStub) Search(context.Context, string, int) ([]backend.SearchResult, error) {
	return nil, nil
}
func (s *agentStub) ListResources(context.Context) ([]backend.ResourceInfo, error) {
	return nil, nil
}
func (s *agentStub) ListAgents() []backend.AgentInfo { return s.agents }
func (s *agentStub) Spawn(ctx context.Context, req backend.SpawnRequest) (string, error) {
	s.lastReq = req
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return s.answer, s.err
}

func TestHandleWrite_AgentHappyPath(t *testing.T) {
	sa := &agentStub{
		name:   "writer",
		agents: []backend.AgentInfo{{ID: "wocha", Description: "workspace writer"}},
		answer: "Appended to memory/2026-04-11.md under 'Bug fix'.",
	}
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{sa},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	out, err := HandleWrite(context.Background(), d, WriteInput{
		Content: "Fixed the auth bug",
		Mode:    "agent",
		Target:  "daily",
	}, model.Caller{ID: "claude", Label: "Claude Code"})
	require.NoError(t, err)
	assert.Equal(t, "written", out.Status)
	assert.Equal(t, "agent", out.Mode)
	assert.Equal(t, "wocha", out.Agent)
	assert.Equal(t, "writer", out.Backend)
	assert.Contains(t, out.Result, "Appended")

	// The raw task passed to Spawn is the content verbatim — no
	// prose-wrapping in the handler anymore, because the backend
	// applies its prompt template.
	assert.Equal(t, "Fixed the auth bug", sa.lastReq.Task)
	assert.Equal(t, "daily", sa.lastReq.Target)
	assert.Equal(t, backend.SpawnPurposeWrite, sa.lastReq.Purpose)
}

func TestHandleWrite_AgentDefaultTarget(t *testing.T) {
	sa := &agentStub{
		name:   "w",
		agents: []backend.AgentInfo{{ID: "alpha"}},
		answer: "ok",
	}
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{sa},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	_, err = HandleWrite(context.Background(), d, WriteInput{
		Content: "content",
		Mode:    "agent",
	}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "auto", sa.lastReq.Target,
		"empty Target should default to \"auto\"")
	assert.Equal(t, backend.SpawnPurposeWrite, sa.lastReq.Purpose)
}

func TestHandleWrite_AgentNoSubagentConfigured(t *testing.T) {
	// Writable filesystem backend only — no subagent.
	d, _ := newWritableDispatcher(t)
	_, err := HandleWrite(context.Background(), d, WriteInput{
		Content: "x",
		Mode:    "agent",
	}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAgentNotFound))
}

func TestHandleWrite_AgentTimeoutSurfacesAsResult(t *testing.T) {
	sa := &agentStub{
		name:   "w",
		agents: []backend.AgentInfo{{ID: "alpha"}},
		answer: "partial draft",
		err:    model.ErrSubagentTimeout,
	}
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{sa},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	out, err := HandleWrite(context.Background(), d, WriteInput{
		Content:        "x",
		Mode:           "agent",
		TimeoutSeconds: 3,
	}, model.AnonymousCaller)
	require.NoError(t, err, "timeout should not escape as error")
	assert.True(t, out.TimedOut)
	assert.Equal(t, "partial draft", out.Result)
}

// TestHandleWrite_AgentFailureReturnsError — a non-timeout subagent
// error must surface as a Go error, not a false {status:"written"}
// success envelope.
//
// Regression test for 0.10.1. Before the handler's switch on
// res.Status, dispatcher.DelegateWrite would encode the error onto
// the task snapshot (Status=failed, Error=...) and return
// (result, nil); handleWriteAgent then hardcoded Status:"written"
// and emitted an empty Result, silently losing the content.
func TestHandleWrite_AgentFailureReturnsError(t *testing.T) {
	sa := &agentStub{
		name:   "w",
		agents: []backend.AgentInfo{{ID: "alpha"}},
		err:    errors.New("network unreachable"),
	}
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{sa},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	_, err = HandleWrite(context.Background(), d, WriteInput{
		Content: "x",
		Mode:    "agent",
	}, model.AnonymousCaller)
	require.Error(t, err, "subagent failure must propagate, not mask as success")
	assert.True(t, errors.Is(err, model.ErrBackendUnavailable),
		"failure should map to backend_unavailable so REST returns 502")
	assert.Contains(t, err.Error(), "network unreachable",
		"underlying error message should survive for operator diagnosis")
}

// TestHandleWrite_AgentRunningOnCallerCancelReturnsError — when the
// caller's ctx cancels before the task completes, the dispatcher
// returns a running snapshot; handleWriteAgent must surface that as
// an error rather than a false success envelope.
//
// Regression test for 0.10.1. The task keeps running in the
// background on the detached task-manager goroutine — that is the
// whole point of the async model — but pf_poke mode:agent is the
// sync-by-default path and cannot claim placement succeeded when
// the subagent has not yet confirmed it.
func TestHandleWrite_AgentRunningOnCallerCancelReturnsError(t *testing.T) {
	block := make(chan struct{})
	// Unblock the background Spawn on test cleanup so the task
	// manager goroutine doesn't linger past the test.
	defer close(block)

	sa := &agentStub{
		name:   "w",
		agents: []backend.AgentInfo{{ID: "alpha"}},
		answer: "ok",
		block:  block,
	}
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{sa},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	// Caller ctx: very short. Task ctx (set via TimeoutSeconds): far
	// longer. The caller ctx fires first, Wait returns ctx.Err, the
	// dispatcher's wait path returns the still-running snapshot.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err = HandleWrite(ctx, d, WriteInput{
		Content:        "x",
		Mode:           "agent",
		TimeoutSeconds: 30,
	}, model.AnonymousCaller)
	require.Error(t, err, "running snapshot must not be reported as written")
	assert.True(t, errors.Is(err, model.ErrBackendUnavailable))
	assert.Contains(t, err.Error(), "did not complete")
}

func TestCallerLabelFor_FallsBackToID(t *testing.T) {
	assert.Equal(t, "label-only", callerLabelFor(model.Caller{Label: "label-only"}))
	assert.Equal(t, "id-fallback", callerLabelFor(model.Caller{ID: "id-fallback"}))
	assert.Equal(t, "", callerLabelFor(model.Caller{}))
}

// Guardrail: the WriteInput/WriteOutput shapes should not accidentally
// drop fields the MCP/REST schemas rely on.
func TestWriteInputOutputShape(t *testing.T) {
	_ = WriteInput{
		URI: "memory://x", Content: "y", Mode: "direct",
		Format: "entry", Agent: "a", Target: "auto", TimeoutSeconds: 10,
	}
	_ = WriteOutput{
		Status: "written", Mode: "direct", URI: "memory://x",
		BytesWritten: 1, Format: "entry", Backend: "fs",
		Agent: "a", ElapsedSeconds: 0, Result: "", TargetsWritten: nil, TimedOut: false,
	}
}

// Ensure the write package's format constants are what the tool
// layer expects. Catches accidental rename/drift.
func TestWriteFormatConstants(t *testing.T) {
	assert.Equal(t, "entry", string(write.EntryFormatEntry))
	assert.Equal(t, "raw", string(write.EntryFormatRaw))
	// Sanity: the tool layer's default is an entry, not an empty
	// string, when caller leaves Format unset.
	assert.True(t, strings.HasPrefix(string(write.EntryFormatEntry), "entry"))
}
