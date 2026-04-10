package backend

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/config"
)

// TestDefaultTemplates_NotEmptyAndDistinct guards against the two
// built-in templates being accidentally blanked or unified. An empty
// default means a fresh subagent gets a bare user query and reverts
// to generic Q&A behaviour — the failure mode this whole subsystem
// exists to prevent.
func TestDefaultTemplates_NotEmptyAndDistinct(t *testing.T) {
	assert.NotEmpty(t, DefaultRetrievePromptTemplate,
		"DefaultRetrievePromptTemplate must not be empty")
	assert.NotEmpty(t, DefaultWritePromptTemplate,
		"DefaultWritePromptTemplate must not be empty")
	assert.NotEqual(t, DefaultRetrievePromptTemplate, DefaultWritePromptTemplate,
		"retrieve and write templates must describe different behaviour")

	// Both templates must reference {task} or WrapTask will produce
	// a prompt that silently drops the user's content.
	assert.Contains(t, DefaultRetrievePromptTemplate, "{task}")
	assert.Contains(t, DefaultWritePromptTemplate, "{task}")

	// The retrieval template must frame the agent as a searcher, not
	// a generic Q&A bot — this is the specific behaviour the real-
	// world deployment feedback reported as missing.
	assert.Contains(t, DefaultRetrievePromptTemplate, "memory-retrieval")
	assert.Contains(t, DefaultWritePromptTemplate, "memory-write")
}

// TestResolvePromptTemplate_Precedence covers the three-layer
// fallback: per-agent override → per-backend default → built-in.
func TestResolvePromptTemplate_Precedence(t *testing.T) {
	t.Run("agent override wins", func(t *testing.T) {
		got := ResolvePromptTemplate("agent-template", "backend-template", SpawnPurposeRetrieve)
		assert.Equal(t, "agent-template", got)
	})
	t.Run("backend default when agent empty", func(t *testing.T) {
		got := ResolvePromptTemplate("", "backend-template", SpawnPurposeRetrieve)
		assert.Equal(t, "backend-template", got)
	})
	t.Run("built-in retrieve default when both empty", func(t *testing.T) {
		got := ResolvePromptTemplate("", "", SpawnPurposeRetrieve)
		assert.Equal(t, DefaultRetrievePromptTemplate, got)
	})
	t.Run("built-in write default when both empty", func(t *testing.T) {
		got := ResolvePromptTemplate("", "", SpawnPurposeWrite)
		assert.Equal(t, DefaultWritePromptTemplate, got)
	})
	t.Run("unknown purpose falls back to retrieve default", func(t *testing.T) {
		got := ResolvePromptTemplate("", "", SpawnPurpose("bogus"))
		assert.Equal(t, DefaultRetrievePromptTemplate, got)
	})
}

// TestWrapTask_PlaceholderSubstitution verifies every documented
// placeholder gets replaced and that unknown placeholders pass
// through untouched.
func TestWrapTask_PlaceholderSubstitution(t *testing.T) {
	tmpl := "agent={agent_id}\ntask={task}\ntarget={target}\ntime={time_range}\nunknown={nope}"
	req := SpawnRequest{
		AgentID:   "alpha",
		Task:      "hello world",
		Target:    "daily",
		TimeRange: "2026-04-01 to 2026-04-11",
	}
	got := WrapTask(tmpl, req)

	assert.Contains(t, got, "agent=alpha")
	assert.Contains(t, got, "task=hello world")
	assert.Contains(t, got, "target=daily")
	assert.Contains(t, got, "TIME RANGE: 2026-04-01 to 2026-04-11")
	assert.Contains(t, got, "unknown={nope}",
		"unknown placeholders should pass through unchanged")
}

// TestWrapTask_EmptyTimeRange guards the "time range line collapses
// to nothing" contract that lets templates include a blank line
// for {time_range} without leaving stray whitespace behind when the
// caller did not provide a range.
func TestWrapTask_EmptyTimeRange(t *testing.T) {
	tmpl := "QUERY:\n{task}\n{time_range}"
	req := SpawnRequest{Task: "hello"}
	got := WrapTask(tmpl, req)

	// Empty time range → template ends with the task and a trailing
	// newline (from the template's literal \n before {time_range}).
	assert.NotContains(t, got, "TIME RANGE",
		"empty time range must not emit a TIME RANGE line")
	assert.Contains(t, got, "QUERY:\nhello")
}

// TestWrapTask_EmptyTemplateEchoesTask is a last-resort fallback:
// a misconfigured backend with an empty template should still
// forward the raw task rather than dispatching an empty string.
func TestWrapTask_EmptyTemplateEchoesTask(t *testing.T) {
	got := WrapTask("", SpawnRequest{Task: "fallback"})
	assert.Equal(t, "fallback", got)
}

// TestWrapTask_DefaultRetrieveTemplate_ProducesMemoryFraming is an
// integration-flavoured check that wrapping a bare query with the
// built-in retrieval template produces the key framing phrases an
// agent needs to see to behave as a memory-retrieval specialist.
func TestWrapTask_DefaultRetrieveTemplate_ProducesMemoryFraming(t *testing.T) {
	got := WrapTask(DefaultRetrievePromptTemplate, SpawnRequest{
		Task: "what did I note about pagefault SSE?",
	})

	// User's query must appear.
	assert.Contains(t, got, "what did I note about pagefault SSE?")
	// Key framing phrases — this is what the subagent reads to know
	// its job is recall, not generation.
	assert.Contains(t, got, "memory-retrieval agent")
	assert.Contains(t, got, "not to generate new content")
	assert.Contains(t, got, "MEMORY.md")
	// Must end or contain "QUERY:" section
	assert.Contains(t, got, "QUERY:")
}

// TestWrapTask_DefaultWriteTemplate_ProducesPlacementFraming —
// parallel check for the write template.
func TestWrapTask_DefaultWriteTemplate_ProducesPlacementFraming(t *testing.T) {
	got := WrapTask(DefaultWritePromptTemplate, SpawnRequest{
		Task:   "fixed auth middleware regression at 3pm",
		Target: "daily",
	})

	assert.Contains(t, got, "fixed auth middleware regression at 3pm")
	assert.Contains(t, got, "daily")
	assert.Contains(t, got, "memory-write agent")
	assert.Contains(t, got, "PERSIST")
	assert.Contains(t, got, "CONTENT TO PERSIST:")
}

// TestAgentPromptOverride_PurposeRouting verifies the helper returns
// the right per-purpose slot on the AgentSpec lookup.
func TestAgentPromptOverride_PurposeRouting(t *testing.T) {
	agents := map[string]agentTemplates{
		"wocha": {
			RetrievePromptTemplate: "retrieve-override",
			WritePromptTemplate:    "write-override",
		},
		"alpha": {}, // no overrides
	}

	assert.Equal(t, "retrieve-override",
		agentPromptOverride(agents, "wocha", SpawnPurposeRetrieve))
	assert.Equal(t, "write-override",
		agentPromptOverride(agents, "wocha", SpawnPurposeWrite))
	assert.Empty(t, agentPromptOverride(agents, "alpha", SpawnPurposeRetrieve),
		"agent with no overrides should return empty string (signals fallback)")
	assert.Empty(t, agentPromptOverride(agents, "ghost", SpawnPurposeRetrieve),
		"unknown agent should return empty string")
}

// TestSubagentCLIBackend_DefaultTemplateAppliedToEcho drives a real
// CLI backend with no template configured and confirms the built-in
// retrieval framing reaches the subprocess. This is the end-to-end
// acceptance test for the prompt-wrap feature: a fresh operator
// installing pagefault should get memory-retrieval framing on
// pf_fault without touching config.
func TestSubagentCLIBackend_DefaultTemplateAppliedToEcho(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("echo semantics differ on Windows")
	}
	// Deliberately not setting RetrievePromptTemplate → the backend
	// should fall through to DefaultRetrievePromptTemplate.
	b, err := NewSubagentCLIBackend(&config.SubagentCLIBackendConfig{
		Name: "sa", Type: "subagent-cli",
		Command: "echo {agent_id}:{task}",
		Timeout: 5,
		Agents:  []config.AgentSpec{{ID: "alpha"}},
	})
	require.NoError(t, err)

	out, err := b.Spawn(t.Context(), SpawnRequest{
		AgentID: "alpha",
		Task:    "what did I note about pagefault SSE?",
	})
	require.NoError(t, err)
	// The echo command prefixes with "alpha:"; after that the full
	// wrapped prompt should appear, starting with the built-in
	// retrieval framing.
	assert.Contains(t, out, "alpha:")
	assert.Contains(t, out, "memory-retrieval agent")
	assert.Contains(t, out, "what did I note about pagefault SSE?")
}
