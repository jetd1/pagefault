package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/audit"
	"github.com/jet/pagefault/internal/backend"
	"github.com/jet/pagefault/internal/filter"
	"github.com/jet/pagefault/internal/model"
)

// mockSubagent implements backend.SubagentBackend for dispatcher tests.
// It records the last Spawn call and returns either a configured answer,
// a timeout, or an error.
type mockSubagent struct {
	name      string
	agents    []backend.AgentInfo
	spawnErr  error
	spawnOut  string
	spawnWait time.Duration

	lastAgentID string
	lastTask    string
	spawns      int
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
func (m *mockSubagent) Spawn(ctx context.Context, agentID, task string, timeout time.Duration) (string, error) {
	m.spawns++
	m.lastAgentID = agentID
	m.lastTask = task
	if m.spawnWait > 0 {
		select {
		case <-time.After(m.spawnWait):
		case <-ctx.Done():
		}
	}
	return m.spawnOut, m.spawnErr
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
	_, err := d.DeepRetrieve(context.Background(), "q", "", time.Second, model.AnonymousCaller)
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

	_, err := d.DeepRetrieve(context.Background(), "q", "does-not-exist", time.Second, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAgentNotFound))
}

func TestDispatcher_DeepRetrieve_DefaultAgent(t *testing.T) {
	a := &mockSubagent{
		name:     "sa",
		agents:   []backend.AgentInfo{{ID: "alpha"}, {ID: "beta"}},
		spawnOut: "hello from alpha",
	}
	d := newSubagentDispatcher(t, a)

	res, err := d.DeepRetrieve(context.Background(), "say hi", "", 5*time.Second, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "hello from alpha", res.Answer)
	assert.Equal(t, "alpha", res.Agent)
	assert.Equal(t, "sa", res.Backend)
	assert.False(t, res.TimedOut)
	assert.Empty(t, res.PartialResult)
	assert.Equal(t, "alpha", a.lastAgentID)
	assert.Equal(t, "say hi", a.lastTask)
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

	res, err := d.DeepRetrieve(context.Background(), "q", "beta", 5*time.Second, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "from beta", res.Answer)
	assert.Equal(t, "beta", res.Agent)
	assert.Equal(t, "http", res.Backend)
	assert.Equal(t, 0, a.spawns, "alpha backend should not have been spawned")
	assert.Equal(t, 1, b.spawns)
}

func TestDispatcher_DeepRetrieve_Timeout(t *testing.T) {
	a := &mockSubagent{
		name:     "sa",
		agents:   []backend.AgentInfo{{ID: "alpha"}},
		spawnOut: "partial",
		spawnErr: fmt.Errorf("%w: test", model.ErrSubagentTimeout),
	}
	d := newSubagentDispatcher(t, a)

	res, err := d.DeepRetrieve(context.Background(), "q", "", time.Second, model.AnonymousCaller)
	require.NoError(t, err, "timeout should not propagate as error")
	assert.True(t, res.TimedOut)
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

	_, err := d.DeepRetrieve(context.Background(), "q", "", time.Second, model.AnonymousCaller)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kaboom")
}

func TestDispatcher_DeepRetrieve_EmptyQuery(t *testing.T) {
	a := &mockSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := newSubagentDispatcher(t, a)

	_, err := d.DeepRetrieve(context.Background(), "", "", time.Second, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}
