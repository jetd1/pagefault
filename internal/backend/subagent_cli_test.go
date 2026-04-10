package backend

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
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

func TestSubagentCLIBackend_Spawn_Echo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("echo semantics differ on Windows")
	}
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command: "echo {agent_id}:{task}",
		Timeout: 10,
		Agents:  []config.AgentSpec{{ID: "alpha"}},
	})
	require.NoError(t, err)

	out, err := b.Spawn(context.Background(), "alpha", "hello world", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "alpha:hello world", out)
}

func TestSubagentCLIBackend_Spawn_DefaultAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("echo semantics differ on Windows")
	}
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command: "echo {agent_id}",
		Agents:  []config.AgentSpec{{ID: "primary"}, {ID: "secondary"}},
	})
	require.NoError(t, err)

	out, err := b.Spawn(context.Background(), "", "task", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "primary", out)
}

func TestSubagentCLIBackend_Spawn_UnknownAgent(t *testing.T) {
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command: "echo x",
		Agents:  []config.AgentSpec{{ID: "alpha"}},
	})
	require.NoError(t, err)

	_, err = b.Spawn(context.Background(), "does-not-exist", "t", time.Second)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAgentNotFound))
}

func TestSubagentCLIBackend_Spawn_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep not portable to Windows")
	}
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command: "sleep 5",
		Agents:  []config.AgentSpec{{ID: "slow"}},
	})
	require.NoError(t, err)

	start := time.Now()
	_, err = b.Spawn(context.Background(), "slow", "t", 100*time.Millisecond)
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
		Command: "false",
		Agents:  []config.AgentSpec{{ID: "fail"}},
	})
	require.NoError(t, err)

	_, err = b.Spawn(context.Background(), "fail", "t", 5*time.Second)
	require.Error(t, err)
	assert.False(t, errors.Is(err, model.ErrSubagentTimeout))
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
