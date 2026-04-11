package tool

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
	"jetd.one/pagefault/internal/dispatcher"
	"jetd.one/pagefault/internal/filter"
	"jetd.one/pagefault/internal/model"
)

// stubSubagent is a zero-dep SubagentBackend for HandleDeepRetrieve
// tests. It returns a configured answer or an error and records the
// last SpawnRequest so tests can assert on purpose, time range, and
// target fields as well as the raw task.
//
// The 0.10.0 async task manager runs Spawn on a background goroutine,
// so tests that inspect lastReq must go through the mutex-guarded
// snapshot() helper to satisfy -race.
type stubSubagent struct {
	name   string
	agents []backend.AgentInfo
	answer string
	err    error

	mu      sync.Mutex
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
	s.mu.Lock()
	s.lastReq = req
	answer := s.answer
	err := s.err
	s.mu.Unlock()
	return answer, err
}

// snapshot returns the last SpawnRequest recorded under the mutex.
func (s *stubSubagent) snapshot() backend.SpawnRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastReq
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

// TestHandleDeepRetrieve_HappyPath — sync compat path (Wait=true).
// The 0.10.0 async default is covered by
// TestHandleDeepRetrieve_AsyncDefaultReturnsTaskID below.
func TestHandleDeepRetrieve_HappyPath(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha", Description: "primary"}},
		answer: "answer text",
	}
	d := makeSubagentDispatcher(t, sa)

	out, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "what is pagefault?", Wait: true}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "answer text", out.Answer)
	assert.Equal(t, "alpha", out.Agent)
	assert.Equal(t, "sa", out.Backend)
	assert.Equal(t, "done", out.Status)
	assert.NotEmpty(t, out.TaskID, "sync path still carries a task_id for audit correlation")
	assert.NotEmpty(t, out.SpawnID)
	assert.True(t, strings.HasPrefix(out.SpawnID, "pf_sp_"))
	assert.GreaterOrEqual(t, out.ElapsedSeconds, 0.0)
	assert.False(t, out.TimedOut)
	assert.Empty(t, out.PartialResult)
	gotReq := sa.snapshot()
	assert.Equal(t, "what is pagefault?", gotReq.Task)
	assert.Equal(t, backend.SpawnPurposeRetrieve, gotReq.Purpose)
	assert.Equal(t, out.SpawnID, gotReq.SpawnID)
}

// TestHandleDeepRetrieve_AsyncDefaultReturnsTaskID — the 0.10.0 wire
// default: no Wait flag, so HandleDeepRetrieve returns immediately
// with a task_id + status=running and the caller must poll pf_ps.
func TestHandleDeepRetrieve_AsyncDefaultReturnsTaskID(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
		answer: "eventually done",
	}
	d := makeSubagentDispatcher(t, sa)

	out, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "q"}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "running", out.Status)
	assert.NotEmpty(t, out.TaskID)
	assert.True(t, strings.HasPrefix(out.TaskID, "pf_tk_"))
	assert.NotEmpty(t, out.SpawnID)
	assert.Empty(t, out.Answer, "async path must not block on the answer")

	// Wait for completion via the dispatcher's task manager so the
	// poll returns a terminal snapshot.
	_, err = d.TaskManager().Wait(context.Background(), out.TaskID)
	require.NoError(t, err)

	// Poll via HandleTaskStatus (the pf_ps(task_id=...) path).
	polled, err := HandleTaskStatus(context.Background(), d,
		ListAgentsInput{TaskID: out.TaskID}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "done", polled.Status)
	assert.Equal(t, "eventually done", polled.Answer)
	assert.Equal(t, out.SpawnID, polled.SpawnID, "spawn_id persists submit → poll")
}

func TestHandleDeepRetrieve_EmptyQuery(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := makeSubagentDispatcher(t, sa)

	_, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "", Wait: true}, model.AnonymousCaller)
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
		DeepRetrieveInput{Query: "q", Agent: "beta", Wait: true}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "beta", out.Agent)
	assert.Equal(t, "beta", sa.snapshot().AgentID)
}

func TestHandleDeepRetrieve_UnknownAgent(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := makeSubagentDispatcher(t, sa)

	_, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "q", Agent: "ghost", Wait: true}, model.AnonymousCaller)
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
		DeepRetrieveInput{Query: "q", TimeoutSeconds: 5, Wait: true}, model.AnonymousCaller)
	require.NoError(t, err, "timeout should not escape as error")
	assert.True(t, out.TimedOut)
	assert.Equal(t, "timed_out", out.Status)
	assert.Equal(t, "partial", out.PartialResult)
	assert.Empty(t, out.Answer)
	assert.Equal(t, 5*time.Second, sa.snapshot().Timeout)
}

func TestHandleDeepRetrieve_DefaultTimeout(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := makeSubagentDispatcher(t, sa)

	_, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "q", Wait: true}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, defaultDeepRetrieveTimeout, sa.snapshot().Timeout)
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
				Wait:           true,
			}, model.AnonymousCaller)
			require.NoError(t, err)
			assert.Equal(t, tc.want, sa.snapshot().TimeRange)
		})
	}
}

func TestHandleDeepRetrieve_NoSubagent(t *testing.T) {
	// Dispatcher with only a fake (non-subagent) backend.
	d := makeDispatcher(t)
	_, err := HandleDeepRetrieve(context.Background(), d,
		DeepRetrieveInput{Query: "q", Wait: true}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAgentNotFound))
}

// TestHandleTaskStatus_UnknownReturnsNotFound — polling a ghost
// task id returns ErrResourceNotFound, which the REST envelope
// translates into a 404.
func TestHandleTaskStatus_UnknownReturnsNotFound(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	d := makeSubagentDispatcher(t, sa)

	_, err := HandleTaskStatus(context.Background(), d,
		ListAgentsInput{TaskID: "pf_tk_nope"}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrResourceNotFound))
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
