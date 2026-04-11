package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jet/pagefault/internal/backend"
)

// ────────────────── result-helper tests ──────────────────

func TestToolResultJSON_EncodesPayload(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	res, err := toolResultJSON(payload{Name: "hello", N: 7})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)

	tc, ok := res.Content[0].(mcppkg.TextContent)
	require.True(t, ok, "expected TextContent, got %T", res.Content[0])
	assert.Equal(t, "text", tc.Type)

	var decoded payload
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &decoded))
	assert.Equal(t, "hello", decoded.Name)
	assert.Equal(t, 7, decoded.N)
}

func TestToolResultJSON_MarshalFailure(t *testing.T) {
	// A channel is not encodable as JSON; encoding/json returns an
	// UnsupportedTypeError which toolResultJSON wraps.
	_, err := toolResultJSON(make(chan int))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal result")
}

func TestToolResultError_IsErrorMarker(t *testing.T) {
	res := toolResultError(errors.New("nope"))
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	tc, ok := res.Content[0].(mcppkg.TextContent)
	require.True(t, ok)
	assert.Equal(t, "nope", tc.Text)
}

// ────────────────── MCP registration tests ──────────────────

// newMCPServerForTest builds an MCPServer with every Phase-1 tool registered
// against a fake in-memory dispatcher.
func newMCPServerForTest(t *testing.T) *mcpserver.MCPServer {
	t.Helper()
	d := makeDispatcher(t) // from tool_test.go
	srv := mcpserver.NewMCPServer("pagefault-test", "0.0.0",
		mcpserver.WithToolCapabilities(true),
	)
	RegisterMCP(srv, d)
	return srv
}

// callTool is a helper that invokes a tool's handler directly through
// ServerTool.Handler. mcp-go exposes this via GetTool so tests don't need a
// live transport.
func callTool(t *testing.T, srv *mcpserver.MCPServer, name string, args map[string]any) *mcppkg.CallToolResult {
	t.Helper()
	tool := srv.GetTool(name)
	require.NotNil(t, tool, "tool %q not registered", name)
	req := mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}
	res, err := tool.Handler(context.Background(), req)
	require.NoError(t, err, "tool handler returned a protocol error")
	require.NotNil(t, res)
	return res
}

// textOf extracts the first text block from a CallToolResult.
func textOf(t *testing.T, res *mcppkg.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(mcppkg.TextContent)
	require.True(t, ok, "expected TextContent, got %T", res.Content[0])
	return tc.Text
}

func TestRegisterMCP_AllToolsRegistered(t *testing.T) {
	srv := newMCPServerForTest(t)
	for _, name := range []string{"pf_maps", "pf_load", "pf_scan", "pf_peek", "pf_fault", "pf_ps", "pf_poke"} {
		assert.NotNil(t, srv.GetTool(name), "tool %q should be registered", name)
	}
}

func TestRegisterMCP_ListContexts(t *testing.T) {
	srv := newMCPServerForTest(t)
	res := callTool(t, srv, "pf_maps", nil)
	assert.False(t, res.IsError)

	var out ListContextsOutput
	require.NoError(t, json.Unmarshal([]byte(textOf(t, res)), &out))
	require.Len(t, out.Contexts, 1)
	assert.Equal(t, "demo", out.Contexts[0].Name)
}

func TestRegisterMCP_GetContext_Success(t *testing.T) {
	srv := newMCPServerForTest(t)
	res := callTool(t, srv, "pf_load", map[string]any{
		"name":   "demo",
		"format": "markdown",
	})
	assert.False(t, res.IsError)

	var out GetContextOutput
	require.NoError(t, json.Unmarshal([]byte(textOf(t, res)), &out))
	assert.Equal(t, "demo", out.Name)
	assert.Equal(t, "markdown", out.Format)
	assert.Contains(t, out.Content, "line1")
	// Happy path — no sources should have been skipped.
	assert.Empty(t, out.SkippedSources)
}

func TestRegisterMCP_GetContext_MissingName(t *testing.T) {
	srv := newMCPServerForTest(t)
	res := callTool(t, srv, "pf_load", map[string]any{})
	require.True(t, res.IsError, "missing name should produce a tool-level error")
	assert.Contains(t, textOf(t, res), "name is required")
}

func TestRegisterMCP_GetContext_UnknownName(t *testing.T) {
	srv := newMCPServerForTest(t)
	res := callTool(t, srv, "pf_load", map[string]any{"name": "does-not-exist"})
	require.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "context not found")
}

func TestRegisterMCP_Search_Success(t *testing.T) {
	srv := newMCPServerForTest(t)
	res := callTool(t, srv, "pf_scan", map[string]any{
		"query": "anything",
		"limit": float64(5), // mcp-go decodes JSON numbers as float64
	})
	assert.False(t, res.IsError)

	var out SearchOutput
	require.NoError(t, json.Unmarshal([]byte(textOf(t, res)), &out))
	require.Len(t, out.Results, 1)
	assert.Equal(t, "memory://foo.md", out.Results[0].URI)
	assert.Equal(t, "fake", out.Results[0].Backend)
}

func TestRegisterMCP_Search_EmptyQuery(t *testing.T) {
	srv := newMCPServerForTest(t)
	res := callTool(t, srv, "pf_scan", map[string]any{"query": ""})
	require.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "query is required")
}

func TestRegisterMCP_Search_BackendsSlice(t *testing.T) {
	// Exercises asStringSlice via []any (the shape JSON decoding produces).
	srv := newMCPServerForTest(t)
	res := callTool(t, srv, "pf_scan", map[string]any{
		"query":    "x",
		"backends": []any{"fake"},
	})
	assert.False(t, res.IsError)
}

func TestRegisterMCP_Read_Success(t *testing.T) {
	srv := newMCPServerForTest(t)
	res := callTool(t, srv, "pf_peek", map[string]any{
		"uri":       "memory://foo.md",
		"from_line": float64(1),
		"to_line":   float64(2),
	})
	assert.False(t, res.IsError)

	var out ReadOutput
	require.NoError(t, json.Unmarshal([]byte(textOf(t, res)), &out))
	require.NotNil(t, out.Resource)
	assert.Equal(t, "memory://foo.md", out.Resource.URI)
	assert.Contains(t, out.Resource.Content, "line1")
}

func TestRegisterMCP_Read_MissingURI(t *testing.T) {
	srv := newMCPServerForTest(t)
	res := callTool(t, srv, "pf_peek", map[string]any{})
	require.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "uri is required")
}

func TestRegisterMCP_Read_UnknownURI(t *testing.T) {
	srv := newMCPServerForTest(t)
	res := callTool(t, srv, "pf_peek", map[string]any{"uri": "memory://nope.md"})
	require.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "resource not found")
}

// ────────────────── pf_fault / pf_ps MCP tests ──────────────────

// newSubagentMCPServer builds an MCP server wired to a dispatcher that
// contains a single stubSubagent. Shared by the pf_fault / pf_ps tests.
func newSubagentMCPServer(t *testing.T, sa *stubSubagent) *mcpserver.MCPServer {
	t.Helper()
	d := makeSubagentDispatcher(t, sa)
	srv := mcpserver.NewMCPServer("pagefault-test", "0.0.0",
		mcpserver.WithToolCapabilities(true),
	)
	RegisterMCP(srv, d)
	return srv
}

func TestRegisterMCP_ListAgents_Empty(t *testing.T) {
	srv := newMCPServerForTest(t) // no subagent backend
	res := callTool(t, srv, "pf_ps", nil)
	assert.False(t, res.IsError)

	var out ListAgentsOutput
	require.NoError(t, json.Unmarshal([]byte(textOf(t, res)), &out))
	assert.NotNil(t, out.Agents)
	assert.Empty(t, out.Agents)
}

func TestRegisterMCP_ListAgents_Populated(t *testing.T) {
	sa := &stubSubagent{
		name: "cli",
		agents: []backend.AgentInfo{
			{ID: "alpha", Description: "primary"},
			{ID: "beta", Description: "secondary"},
		},
	}
	srv := newSubagentMCPServer(t, sa)

	res := callTool(t, srv, "pf_ps", nil)
	assert.False(t, res.IsError)

	var out ListAgentsOutput
	require.NoError(t, json.Unmarshal([]byte(textOf(t, res)), &out))
	require.Len(t, out.Agents, 2)
	assert.Equal(t, "alpha", out.Agents[0].ID)
	assert.Equal(t, "primary", out.Agents[0].Description)
	assert.Equal(t, "cli", out.Agents[0].Backend)
}

// TestRegisterMCP_DeepRetrieve_Success — sync wait path. The call
// passes wait=true so the MCP tool returns the populated answer
// inline. The async-default is covered by
// TestRegisterMCP_DeepRetrieve_AsyncReturnsTaskID below.
func TestRegisterMCP_DeepRetrieve_Success(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
		answer: "42",
	}
	srv := newSubagentMCPServer(t, sa)

	res := callTool(t, srv, "pf_fault", map[string]any{
		"query":           "what?",
		"agent":           "alpha",
		"timeout_seconds": float64(5),
		"wait":            true,
	})
	assert.False(t, res.IsError)

	var out DeepRetrieveOutput
	require.NoError(t, json.Unmarshal([]byte(textOf(t, res)), &out))
	assert.Equal(t, "42", out.Answer)
	assert.Equal(t, "alpha", out.Agent)
	assert.Equal(t, "sa", out.Backend)
	assert.Equal(t, "done", out.Status)
	assert.NotEmpty(t, out.TaskID)
	assert.False(t, out.TimedOut)
}

// TestRegisterMCP_DeepRetrieve_AsyncReturnsTaskID — the 0.10.0
// default: no wait flag, pf_fault returns {task_id, status:running}
// and pf_ps(task_id) is used to poll for the terminal snapshot.
// End-to-end MCP exercise of the async polling pattern.
func TestRegisterMCP_DeepRetrieve_AsyncReturnsTaskID(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
		answer: "async 42",
	}
	srv := newSubagentMCPServer(t, sa)

	res := callTool(t, srv, "pf_fault", map[string]any{
		"query": "what?",
	})
	assert.False(t, res.IsError)

	var out DeepRetrieveOutput
	require.NoError(t, json.Unmarshal([]byte(textOf(t, res)), &out))
	assert.Equal(t, "running", out.Status)
	assert.NotEmpty(t, out.TaskID)

	// Poll via pf_ps(task_id=...) — the Mode B path.
	// Retry a few times because the Run goroutine may not have
	// completed by the first poll in loaded CI environments.
	var polled DeepRetrieveOutput
	for i := 0; i < 20; i++ {
		pollRes := callTool(t, srv, "pf_ps", map[string]any{"task_id": out.TaskID})
		require.False(t, pollRes.IsError)
		require.NoError(t, json.Unmarshal([]byte(textOf(t, pollRes)), &polled))
		if polled.Status == "done" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	assert.Equal(t, "done", polled.Status)
	assert.Equal(t, "async 42", polled.Answer)
	assert.Equal(t, out.TaskID, polled.TaskID)
}

// TestRegisterMCP_ListAgents_UnknownTaskID — the Mode B path when
// the task id does not exist (never submitted or TTL-expired)
// returns a resource_not_found error through the MCP envelope.
func TestRegisterMCP_ListAgents_UnknownTaskID(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	srv := newSubagentMCPServer(t, sa)

	res := callTool(t, srv, "pf_ps", map[string]any{"task_id": "pf_tk_ghost"})
	require.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "task")
}

func TestRegisterMCP_DeepRetrieve_MissingQuery(t *testing.T) {
	sa := &stubSubagent{
		name:   "sa",
		agents: []backend.AgentInfo{{ID: "alpha"}},
	}
	srv := newSubagentMCPServer(t, sa)

	res := callTool(t, srv, "pf_fault", map[string]any{})
	require.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "query is required")
}

func TestRegisterMCP_DeepRetrieve_NoSubagentConfigured(t *testing.T) {
	srv := newMCPServerForTest(t) // only a fake/filesystem-like backend
	res := callTool(t, srv, "pf_fault", map[string]any{"query": "hi"})
	require.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "agent not found")
}

// ────────────────── pf_poke MCP tests ──────────────────

// newWritableMCPServer wires MCP against a dispatcher with a writable
// filesystem backend rooted at a tempdir. Used by the pf_poke MCP
// tests so the handler's argument unmarshaling and direct-mode
// round-trip are exercised end-to-end.
func newWritableMCPServer(t *testing.T) *mcpserver.MCPServer {
	t.Helper()
	d, _ := newWritableDispatcher(t)
	srv := mcpserver.NewMCPServer("pagefault-test", "0.0.0",
		mcpserver.WithToolCapabilities(true),
	)
	RegisterMCP(srv, d)
	return srv
}

func TestRegisterMCP_Write_DirectSuccess(t *testing.T) {
	srv := newWritableMCPServer(t)
	res := callTool(t, srv, "pf_poke", map[string]any{
		"uri":     "memory://notes/x.md",
		"content": "hello from mcp",
		"mode":    "direct",
	})
	assert.False(t, res.IsError)

	var out WriteOutput
	require.NoError(t, json.Unmarshal([]byte(textOf(t, res)), &out))
	assert.Equal(t, "written", out.Status)
	assert.Equal(t, "direct", out.Mode)
	assert.Equal(t, "memory://notes/x.md", out.URI)
	assert.Positive(t, out.BytesWritten)
}

func TestRegisterMCP_Write_MissingMode(t *testing.T) {
	srv := newWritableMCPServer(t)
	res := callTool(t, srv, "pf_poke", map[string]any{
		"uri":     "memory://notes/x.md",
		"content": "x",
	})
	require.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "mode is required")
}

func TestRegisterMCP_Write_MissingContent(t *testing.T) {
	srv := newWritableMCPServer(t)
	res := callTool(t, srv, "pf_poke", map[string]any{
		"uri":  "memory://notes/x.md",
		"mode": "direct",
	})
	require.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "content is required")
}

func TestRegisterMCP_Write_DirectReadOnlyBackend(t *testing.T) {
	// newMCPServerForTest uses the non-writable fakeBackend. A write
	// against it should surface access_violation through the MCP
	// result envelope.
	srv := newMCPServerForTest(t)
	res := callTool(t, srv, "pf_poke", map[string]any{
		"uri":     "memory://notes/x.md",
		"content": "x",
		"mode":    "direct",
	})
	require.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "read-only")
}
