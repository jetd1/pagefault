package backend

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"jetd.one/pagefault/internal/config"
	"jetd.one/pagefault/internal/model"
)

func TestTokenizeCommand(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
		err  bool
	}{
		{"simple", "foo bar baz", []string{"foo", "bar", "baz"}, false},
		{"extra-spaces", "foo   bar  ", []string{"foo", "bar"}, false},
		{"single-quoted", "foo 'hello world'", []string{"foo", "hello world"}, false},
		{"double-quoted", `foo "hello world"`, []string{"foo", "hello world"}, false},
		{"mixed-quotes", `foo 'it"s' bar`, []string{"foo", `it"s`, "bar"}, false},
		{"placeholder", "cmd --task {task} --id {agent_id}", []string{"cmd", "--task", "{task}", "--id", "{agent_id}"}, false},
		{"unterminated-single", "foo 'bar", nil, true},
		{"unterminated-double", `foo "bar`, nil, true},
		{"empty", "", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tokenizeCommand(tt.in)
			if tt.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewSubagentCLIBackend_Validation(t *testing.T) {
	t.Run("nil-config", func(t *testing.T) {
		_, err := NewSubagentCLIBackend(nil)
		require.Error(t, err)
	})
	t.Run("empty-command", func(t *testing.T) {
		_, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
			Name: "x", Type: "subagent-cli", Command: "   ",
			Agents: []config.AgentSpec{{ID: "a"}},
		})
		require.Error(t, err)
	})
	t.Run("no-agents", func(t *testing.T) {
		_, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
			Name: "x", Type: "subagent-cli", Command: "echo hi",
		})
		require.Error(t, err)
	})
	t.Run("duplicate-agents", func(t *testing.T) {
		_, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
			Name: "x", Type: "subagent-cli", Command: "echo hi",
			Agents: []config.AgentSpec{{ID: "a"}, {ID: "a"}},
		})
		require.Error(t, err)
	})
	t.Run("unterminated-quote", func(t *testing.T) {
		_, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
			Name: "x", Type: "subagent-cli", Command: "echo 'hi",
			Agents: []config.AgentSpec{{ID: "a"}},
		})
		require.Error(t, err)
	})
	t.Run("ok", func(t *testing.T) {
		b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
			Name: "sa", Type: "subagent-cli",
			Command: "echo {task}",
			Timeout: 10,
			Agents: []config.AgentSpec{
				{ID: "alpha", Description: "alpha agent"},
				{ID: "beta", Description: "beta agent"},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "sa", b.Name())
		assert.Equal(t, "alpha", b.DefaultAgentID())
		agents := b.ListAgents()
		require.Len(t, agents, 2)
		assert.Equal(t, "alpha", agents[0].ID)
		assert.Equal(t, "alpha agent", agents[0].Description)
	})
}

// passthroughTmpl is a minimal {task} template used by tests that
// care about the plumbing (argv substitution, timeout, agent
// selection) rather than the default prompt-wrap framing. Without
// it, every Spawn output would start with the full
// DefaultRetrievePromptTemplate and every assertion would have to
// grep for the echoed task inside the wrapped text.
const passthroughTmpl = "{task}"

func TestSubagentCLIBackend_Spawn_Echo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("echo semantics differ on Windows")
	}
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command:                "echo {agent_id}:{task}",
		Timeout:                10,
		Agents:                 []config.AgentSpec{{ID: "alpha"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	out, err := b.Spawn(context.Background(), SpawnRequest{
		AgentID: "alpha",
		Task:    "hello world",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, "alpha:hello world", out)
}

func TestSubagentCLIBackend_Spawn_DefaultAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("echo semantics differ on Windows")
	}
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command:                "echo {agent_id}",
		Agents:                 []config.AgentSpec{{ID: "primary"}, {ID: "secondary"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	out, err := b.Spawn(context.Background(), SpawnRequest{
		Task:    "task",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, "primary", out)
}

func TestSubagentCLIBackend_Spawn_UnknownAgent(t *testing.T) {
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command:                "echo x",
		Agents:                 []config.AgentSpec{{ID: "alpha"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	_, err = b.Spawn(context.Background(), SpawnRequest{
		AgentID: "does-not-exist",
		Task:    "t",
		Timeout: time.Second,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAgentNotFound))
}

func TestSubagentCLIBackend_Spawn_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep not portable to Windows")
	}
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command:                "sleep 5",
		Agents:                 []config.AgentSpec{{ID: "slow"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	start := time.Now()
	_, err = b.Spawn(context.Background(), SpawnRequest{
		AgentID: "slow",
		Task:    "t",
		Timeout: 100 * time.Millisecond,
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrSubagentTimeout),
		"expected ErrSubagentTimeout, got %v", err)
	assert.Less(t, elapsed, 3*time.Second, "process should have been killed")
}

func TestSubagentCLIBackend_Spawn_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("false command not portable to Windows")
	}
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command:                "false",
		Agents:                 []config.AgentSpec{{ID: "fail"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	_, err = b.Spawn(context.Background(), SpawnRequest{
		AgentID: "fail",
		Task:    "t",
		Timeout: 5 * time.Second,
	})
	require.Error(t, err)
	assert.False(t, errors.Is(err, model.ErrSubagentTimeout))
}

// TestSubagentCLIBackend_Spawn_SpawnIDPassthrough — the 0.10.0
// {spawn_id} placeholder is substituted into the argv so operators
// can wire it into `openclaw agent run --session-id {spawn_id}` (or
// any other command flag that wants a unique per-call token).
// Operators who do not include {spawn_id} in the command are
// unaffected; the substitution is silently a no-op for them.
func TestSubagentCLIBackend_Spawn_SpawnIDPassthrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("echo semantics differ on Windows")
	}
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command:                "echo session={spawn_id}",
		Agents:                 []config.AgentSpec{{ID: "alpha"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	out, err := b.Spawn(context.Background(), SpawnRequest{
		AgentID: "alpha",
		Task:    "t",
		SpawnID: "pf_sp_fixture",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, "session=pf_sp_fixture", out)
}

// TestSubagentCLIBackend_Spawn_SpawnIDEmptyWhenUnused — a backend
// whose command does not reference {spawn_id} works identically
// regardless of whether the caller supplies SpawnID. Guards the
// 0.9.x→0.10.0 backwards-compat promise.
func TestSubagentCLIBackend_Spawn_SpawnIDEmptyWhenUnused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("echo semantics differ on Windows")
	}
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command:                "echo {task}",
		Agents:                 []config.AgentSpec{{ID: "alpha"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	out, err := b.Spawn(context.Background(), SpawnRequest{
		AgentID: "alpha",
		Task:    "hi",
		SpawnID: "pf_sp_unused",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, "hi", out)
}

func TestSubagentCLIBackend_NoopRead(t *testing.T) {
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command: "echo x",
		Agents:  []config.AgentSpec{{ID: "alpha"}},
	})
	require.NoError(t, err)

	_, err = b.Read(context.Background(), "x://y")
	assert.True(t, errors.Is(err, model.ErrResourceNotFound))

	res, err := b.Search(context.Background(), "q", 10)
	require.NoError(t, err)
	assert.Nil(t, res)

	list, err := b.ListResources(context.Background())
	require.NoError(t, err)
	assert.Nil(t, list)
}
