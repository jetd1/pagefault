package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"jetd.one/pagefault/internal/audit"
	"jetd.one/pagefault/internal/backend"
	"jetd.one/pagefault/internal/filter"
	"jetd.one/pagefault/internal/model"
	"jetd.one/pagefault/internal/task"
)

// mockSubagent implements backend.SubagentBackend for dispatcher tests.
// It records the last Spawn call and returns either a configured answer,
// a timeout, or an error. The recorded request lets tests assert that
// the dispatcher populated the purpose / time range / target fields
// correctly before calling into the backend.
//
// The mock takes an internal mutex because 0.10.0's task manager
// runs Spawn on a background goroutine — tests that inspect
// lastReq under -race need the write and read to happen under the
// same lock.
type mockSubagent struct {
	name      string
	agents    []backend.AgentInfo
	spawnErr  error
	spawnOut  string
	spawnWait time.Duration

	mu      sync.Mutex
	lastReq backend.SpawnRequest
	spawns  int
}

func (m *mockSubagent) Name() string { return m.name }
func (m *mockSubagent) Read(context.Context, string) (*backend.Resource, error) {
	return nil, model.ErrResourceNotFound
}
func (m *mockSubagent) Search(context.Context, string, int) ([]backend.SearchResult, error) {
	return nil, nil
}
func (m *mockSubagent) ListResources(context.Context) ([]backend.ResourceInfo, error) {
	return nil, nil
}
func (m *mockSubagent) ListAgents() []backend.AgentInfo {
	return append([]backend.AgentInfo(nil), m.agents...)
}
func (m *mockSubagent) Spawn(ctx context.Context, req backend.SpawnRequest) (string, error) {
	m.mu.Lock()
	m.spawns++
	m.lastReq = req
	wait := m.spawnWait
	out := m.spawnOut
	err := m.spawnErr
	m.mu.Unlock()
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	return out, err
}

// snapshot returns the last recorded Spawn request under the mutex.
// Tests must use this instead of touching m.lastReq directly.
func (m *mockSubagent) snapshot() (backend.SpawnRequest, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastReq, m.spawns
}

func newSubagentDispatcher(t *testing.T, subs ...backend.Backend) *ToolDispatcher {
	t.Helper()
	d, err := New(Options{
		Backends: subs,
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)
	return d
}

func TestDispatcher_ListAgents_Empty(t *testing.T) {
	d, _ := newTestDispatcher(t) // reuses filesystem-like mock, no subagent
	out, err := d.ListAgents(context.Background(), model.AnonymousCaller)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestDispatcher_ListAgents_AcrossBackends(t *testing.T) {
	a := &mockSubagent{
		name: "cli",
		agents: []backend.AgentInfo{
			{ID: "alpha", Description: "primary"},
			{ID: "beta", Description: "secondary"},
		},
	}
	b := &mockSubagent{
		name: "http",
		agents: []backend.AgentInfo{
			{ID: "gamma", Description: "http agent"},
		},
	}
	d := newSubagentDispatcher(t, a, b)

	out, err := d.ListAgents(context.Background(), model.AnonymousCaller)
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.Equal(t, "alpha", out[0].ID)
	assert.Equal(t, "cli", out[0].Backend)
	assert.Equal(t, "beta", out[1].ID)
	assert.Equal(t, "gamma", out[2].ID)
	assert.Equal(t, "http", out[2].Backend)
}

func TestDispatcher_DeepRetrieve_NoSubagent(t *testing.T) {
	d, _ := newTestDispatcher(t)
	_, err := d.DeepRetrieve(context.Background(), "q", "", time.Second, model.AnonymousCaller, DeepRetrieveOptions{Wait: true})
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAgentNotFound))
	assert.Contains(t, err.Error(), "no subagent backend")
}

func TestDispatcher_DeepRetrieve_UnknownAgent(t *testing.T) {
	a := &mockSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := newSubagentDispatcher(t, a)

	_, err := d.DeepRetrieve(context.Background(), "q", "does-not-exist", time.Second, model.AnonymousCaller, DeepRetrieveOptions{Wait: true})
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAgentNotFound))
}

// TestDispatcher_DeepRetrieve_DefaultAgent — happy-path sync (Wait=true)
// call picks the first agent and returns the subagent's answer inline.
func TestDispatcher_DeepRetrieve_DefaultAgent(t *testing.T) {
	a := &mockSubagent{
		name:     "sa",
		agents:   []backend.AgentInfo{{ID: "alpha"}, {ID: "beta"}},
		spawnOut: "hello from alpha",
	}
	d := newSubagentDispatcher(t, a)

	res, err := d.DeepRetrieve(context.Background(), "say hi", "", 5*time.Second, model.AnonymousCaller, DeepRetrieveOptions{Wait: true})
	require.NoError(t, err)
	assert.Equal(t, "hello from alpha", res.Answer)
	assert.Equal(t, "alpha", res.Agent)
	assert.Equal(t, "sa", res.Backend)
	assert.Equal(t, "done", res.Status)
	assert.False(t, res.TimedOut)
	assert.Empty(t, res.PartialResult)
	assert.NotEmpty(t, res.TaskID, "sync path still records the task_id")
	assert.NotEmpty(t, res.SpawnID, "spawn id is surfaced to the caller")
	assert.True(t, strings.HasPrefix(res.SpawnID, "pf_sp_"))

	gotReq, gotSpawns := a.snapshot()
	assert.Equal(t, "alpha", gotReq.AgentID)
	assert.Equal(t, "say hi", gotReq.Task)
	assert.Equal(t, backend.SpawnPurposeRetrieve, gotReq.Purpose)
	assert.Equal(t, res.SpawnID, gotReq.SpawnID, "spawn_id plumbs through to the backend")
	assert.Equal(t, 1, gotSpawns)
}

func TestDispatcher_DeepRetrieve_ExplicitAgentOnSecondBackend(t *testing.T) {
	a := &mockSubagent{
		name:   "cli",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	b := &mockSubagent{
		name:     "http",
		agents:   []backend.AgentInfo{{ID: "beta"}},
		spawnOut: "from beta",
	}
	d := newSubagentDispatcher(t, a, b)

	res, err := d.DeepRetrieve(context.Background(), "q", "beta", 5*time.Second, model.AnonymousCaller, DeepRetrieveOptions{Wait: true})
	require.NoError(t, err)
	assert.Equal(t, "from beta", res.Answer)
	assert.Equal(t, "beta", res.Agent)
	assert.Equal(t, "http", res.Backend)
	_, aSpawns := a.snapshot()
	_, bSpawns := b.snapshot()
	assert.Equal(t, 0, aSpawns, "alpha backend should not have been spawned")
	assert.Equal(t, 1, bSpawns)
}

func TestDispatcher_DeepRetrieve_Timeout(t *testing.T) {
	a := &mockSubagent{
		name:     "sa",
		agents:   []backend.AgentInfo{{ID: "alpha"}},
		spawnOut: "partial",
		spawnErr: fmt.Errorf("%w: test", model.ErrSubagentTimeout),
	}
	d := newSubagentDispatcher(t, a)

	res, err := d.DeepRetrieve(context.Background(), "q", "", time.Second, model.AnonymousCaller, DeepRetrieveOptions{Wait: true})
	require.NoError(t, err, "timeout should not propagate as error")
	assert.True(t, res.TimedOut)
	assert.Equal(t, "timed_out", res.Status)
	assert.Equal(t, "partial", res.PartialResult)
	assert.Empty(t, res.Answer)
	assert.Equal(t, "alpha", res.Agent)
}

func TestDispatcher_DeepRetrieve_BackendError(t *testing.T) {
	a := &mockSubagent{
		name:     "sa",
		agents:   []backend.AgentInfo{{ID: "alpha"}},
		spawnErr: errors.New("kaboom"),
	}
	d := newSubagentDispatcher(t, a)

	// In 0.10.0 a non-timeout backend error no longer propagates
	// as a Go-level error from DeepRetrieve — it is recorded on
	// the task with Status=failed. The caller reads .Error on the
	// result instead of relying on the `err` return.
	res, err := d.DeepRetrieve(context.Background(), "q", "", time.Second, model.AnonymousCaller, DeepRetrieveOptions{Wait: true})
	require.NoError(t, err)
	assert.Equal(t, "failed", res.Status)
	assert.Contains(t, res.Error, "kaboom")
}

func TestDispatcher_DeepRetrieve_EmptyQuery(t *testing.T) {
	a := &mockSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := newSubagentDispatcher(t, a)

	_, err := d.DeepRetrieve(context.Background(), "", "", time.Second, model.AnonymousCaller, DeepRetrieveOptions{Wait: true})
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

// TestDispatcher_DeepRetrieve_TimeRangePassthrough guards the time
// range handoff from the dispatcher options to the SpawnRequest so
// the backend's prompt template can interpolate it. Without this
// plumbing, TimeRangeStart/End on pf_fault would silently drop on
// the floor.
func TestDispatcher_DeepRetrieve_TimeRangePassthrough(t *testing.T) {
	a := &mockSubagent{
		name:     "sa",
		agents:   []backend.AgentInfo{{ID: "alpha"}},
		spawnOut: "ok",
	}
	d := newSubagentDispatcher(t, a)

	_, err := d.DeepRetrieve(context.Background(), "q", "", time.Second, model.AnonymousCaller,
		DeepRetrieveOptions{Wait: true, TimeRange: "2026-04-01 to 2026-04-11"})
	require.NoError(t, err)
	gotReq, _ := a.snapshot()
	assert.Equal(t, "2026-04-01 to 2026-04-11", gotReq.TimeRange)
}

// TestDispatcher_DeepRetrieve_Async — the 0.10.0 default: no Wait
// flag, so DeepRetrieve returns immediately with a running snapshot
// and the caller must poll GetTask for the final answer.
func TestDispatcher_DeepRetrieve_Async(t *testing.T) {
	a := &mockSubagent{
		name:      "sa",
		agents:    []backend.AgentInfo{{ID: "alpha"}},
		spawnOut:  "async answer",
		spawnWait: 50 * time.Millisecond,
	}
	d := newSubagentDispatcher(t, a)

	res, err := d.DeepRetrieve(context.Background(), "q", "", 5*time.Second, model.AnonymousCaller, DeepRetrieveOptions{})
	require.NoError(t, err)
	assert.Equal(t, "running", res.Status)
	assert.NotEmpty(t, res.TaskID)
	assert.True(t, strings.HasPrefix(res.TaskID, "pf_tk_"))
	assert.NotEmpty(t, res.SpawnID)
	assert.Empty(t, res.Answer, "async path must not block for the answer")

	// Wait for completion via the manager, then poll GetTask.
	final, err := d.TaskManager().Wait(context.Background(), res.TaskID)
	require.NoError(t, err)
	assert.Equal(t, task.StatusDone, final.Status)

	polled, err := d.GetTask(context.Background(), res.TaskID, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "done", polled.Status)
	assert.Equal(t, "async answer", polled.Answer)
	assert.Equal(t, res.SpawnID, polled.SpawnID, "spawn_id persists across submit → poll")
}

// TestDispatcher_GetTask_Unknown — polling a task id that was never
// submitted (or aged out past the TTL) returns ErrResourceNotFound.
func TestDispatcher_GetTask_Unknown(t *testing.T) {
	a := &mockSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := newSubagentDispatcher(t, a)

	_, err := d.GetTask(context.Background(), "pf_tk_ghost", model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrResourceNotFound))
}

// TestDispatcher_DelegateWrite drives the pf_poke mode:agent path
// through the dispatcher and asserts the SpawnRequest carries
// SpawnPurposeWrite plus the target hint. This is what makes a
// subagent see the write-framed prompt template rather than the
// retrieval-framed one.
func TestDispatcher_DelegateWrite(t *testing.T) {
	a := &mockSubagent{
		name:     "sa",
		agents:   []backend.AgentInfo{{ID: "alpha"}},
		spawnOut: "persisted to notes/foo.md",
	}
	d := newSubagentDispatcher(t, a)

	res, err := d.DelegateWrite(context.Background(),
		"fixed the auth regression at 3pm",
		"", 5*time.Second, model.AnonymousCaller,
		DelegateWriteOptions{Wait: true, Target: "daily"})
	require.NoError(t, err)
	assert.Equal(t, "persisted to notes/foo.md", res.Answer)
	assert.Equal(t, "done", res.Status)
	gotReq, _ := a.snapshot()
	assert.Equal(t, backend.SpawnPurposeWrite, gotReq.Purpose)
	assert.Equal(t, "daily", gotReq.Target)
	assert.Equal(t, "fixed the auth regression at 3pm", gotReq.Task)
	assert.NotEmpty(t, gotReq.SpawnID)
}

// TestDispatcher_DelegateWrite_EmptyContent — validation guard.
func TestDispatcher_DelegateWrite_EmptyContent(t *testing.T) {
	a := &mockSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := newSubagentDispatcher(t, a)

	_, err := d.DelegateWrite(context.Background(), "", "", time.Second, model.AnonymousCaller, DelegateWriteOptions{Wait: true})
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}
