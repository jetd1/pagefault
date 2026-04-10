package tool

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
	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/filter"
	"github.com/jet/pagefault/internal/model"
)

// stubSubagent is a zero-dep SubagentBackend for HandleDeepRetrieve
// tests. It returns a configured answer or an error and records the
// last SpawnRequest so tests can assert on purpose, time range, and
// target fields as well as the raw task.
type stubSubagent struct {
	name   string
	agents []backend.AgentInfo
	answer string
	err    error

	lastReq backend.SpawnRequest
}

func (s *stubSubagent) Name() string { return s.name }
func (s *stubSubagent) Read(context.Context, string) (*backend.Resource, error) {
	return nil, model.ErrResourceNotFound
}
func (s *stubSubagent) Search(context.Context, string, int) ([]backend.SearchResult, error) {
	return nil, nil
}
func (s *stubSubagent) ListResources(context.Context) ([]backend.ResourceInfo, error) {
	return nil, nil
}
func (s *stubSubagent) ListAgents() []backend.AgentInfo { return s.agents }
func (s *stubSubagent) Spawn(_ context.Context, req backend.SpawnRequest) (string, error) {
	s.lastReq = req
	return s.answer, s.err
}

func makeSubagentDispatcher(t *testing.T, sa *stubSubagent) *dispatcher.ToolDispatcher {
	t.Helper()
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{sa},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)
	return d
}

func TestHandleDeepRetrieve_HappyPath(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha", Description: "primary"}},
		answer: "answer text",
	}
	d := makeSubagentDispatcher(t, sa)

	out, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "what is pagefault?"}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "answer text", out.Answer)
	assert.Equal(t, "alpha", out.Agent)
	assert.Equal(t, "sa", out.Backend)
	assert.GreaterOrEqual(t, out.ElapsedSeconds, 0.0)
	assert.False(t, out.TimedOut)
	assert.Empty(t, out.PartialResult)
	assert.Equal(t, "what is pagefault?", sa.lastReq.Task)
	assert.Equal(t, backend.SpawnPurposeRetrieve, sa.lastReq.Purpose)
}

func TestHandleDeepRetrieve_EmptyQuery(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := makeSubagentDispatcher(t, sa)

	_, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: ""}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestHandleDeepRetrieve_ExplicitAgent(t *testing.T) {
	sa := &stubSubagent{
		name: "sa",
		agents: []backend.AgentInfo{
			{ID: "alpha"}, {ID: "beta"},
		},
		answer: "beta says hi",
	}
	d := makeSubagentDispatcher(t, sa)

	out, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "q", Agent: "beta"}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "beta", out.Agent)
	assert.Equal(t, "beta", sa.lastReq.AgentID)
}

func TestHandleDeepRetrieve_UnknownAgent(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := makeSubagentDispatcher(t, sa)

	_, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "q", Agent: "ghost"}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAgentNotFound))
}

func TestHandleDeepRetrieve_Timeout(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
		answer: "partial",
		err:    fmt.Errorf("%w: simulated", model.ErrSubagentTimeout),
	}
	d := makeSubagentDispatcher(t, sa)

	out, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "q", TimeoutSeconds: 5}, model.AnonymousCaller)
	require.NoError(t, err, "timeout should not escape as error")
	assert.True(t, out.TimedOut)
	assert.Equal(t, "partial", out.PartialResult)
	assert.Empty(t, out.Answer)
	assert.Equal(t, 5*time.Second, sa.lastReq.Timeout)
}

func TestHandleDeepRetrieve_DefaultTimeout(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := makeSubagentDispatcher(t, sa)

	_, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "q"}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, defaultDeepRetrieveTimeout, sa.lastReq.Timeout)
}

// TestHandleDeepRetrieve_TimeRangePassthrough confirms the
// TimeRangeStart / TimeRangeEnd input fields flow all the way through
// to the SpawnRequest.TimeRange seen by the backend, including the
// "from X to Y" / "from X onwards" / "up to Y" formatting that
// formatTimeRange applies.
func TestHandleDeepRetrieve_TimeRangePassthrough(t *testing.T) {
	cases := []struct {
		name  string
		start string
		end   string
		want  string
	}{
		{"both", "2026-04-01", "2026-04-11", "2026-04-01 to 2026-04-11"},
		{"start only", "2026-04-01", "", "from 2026-04-01 onwards"},
		{"end only", "", "2026-04-11", "up to 2026-04-11"},
		{"neither", "", "", ""},
		{"whitespace-only end", "2026-04-01", "   ", "from 2026-04-01 onwards"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sa := &stubSubagent{
				name:   "sa",
				agents: []backend.AgentInfo{{ID: "alpha"}},
				answer: "ok",
			}
			d := makeSubagentDispatcher(t, sa)
			_, err := HandleDeepRetrieve(context.Background(), d, DeepRetrieveInput{
				Query:          "q",
				TimeRangeStart: tc.start,
				TimeRangeEnd:   tc.end,
			}, model.AnonymousCaller)
			require.NoError(t, err)
			assert.Equal(t, tc.want, sa.lastReq.TimeRange)
		})
	}
}

func TestHandleDeepRetrieve_NoSubagent(t *testing.T) {
	// Dispatcher with only a fake (non-subagent) backend.
	d := makeDispatcher(t)
	_, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "q"}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAgentNotFound))
}

func TestHandleListAgents_Empty(t *testing.T) {
	d := makeDispatcher(t)
	out, err := HandleListAgents(context.Background(), d, ListAgentsInput{}, model.AnonymousCaller)
	require.NoError(t, err)
	require.NotNil(t, out.Agents)
	assert.Len(t, out.Agents, 0)
}

func TestHandleListAgents_Populated(t *testing.T) {
	sa := &stubSubagent{
		name: "sa",
		agents: []backend.AgentInfo{
			{ID: "alpha", Description: "a"},
			{ID: "beta", Description: "b"},
		},
	}
	d := makeSubagentDispatcher(t, sa)

	out, err := HandleListAgents(context.Background(), d, ListAgentsInput{}, model.AnonymousCaller)
	require.NoError(t, err)
	require.Len(t, out.Agents, 2)
	assert.Equal(t, "alpha", out.Agents[0].ID)
	assert.Equal(t, "a", out.Agents[0].Description)
	assert.Equal(t, "sa", out.Agents[0].Backend)
}
