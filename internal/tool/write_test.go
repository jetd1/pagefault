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
// tests. It records the task it saw and returns a canned answer.
type agentStub struct {
	name     string
	agents   []backend.AgentInfo
	answer   string
	err      error
	lastTask string
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
func (s *agentStub) Spawn(_ context.Context, _, task string, _ time.Duration) (string, error) {
	s.lastTask = task
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

	// Verify the composed task contains the key pieces.
	assert.Contains(t, sa.lastTask, "Claude Code")
	assert.Contains(t, sa.lastTask, "Fixed the auth bug")
	assert.Contains(t, sa.lastTask, "daily")
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
	assert.Contains(t, sa.lastTask, `Target: "auto"`)
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

func TestComposeAgentWriteTask_Format(t *testing.T) {
	got := composeAgentWriteTask("body", "long-term", model.Caller{ID: "a", Label: "Agent"})
	assert.Contains(t, got, "Agent")
	assert.Contains(t, got, `"body"`)
	assert.Contains(t, got, `Target: "long-term"`)
	assert.Contains(t, got, "existing file conventions")
}

func TestComposeAgentWriteTask_FallsBackToID(t *testing.T) {
	got := composeAgentWriteTask("x", "auto", model.Caller{ID: "cli", Label: ""})
	assert.Contains(t, got, "(cli)")
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
