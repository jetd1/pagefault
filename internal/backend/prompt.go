package backend

import (
	"strings"
	"time"
)

// SpawnPurpose distinguishes the two reasons pagefault spawns a
// subagent: a pf_fault retrieval call vs a pf_poke mode:"agent"
// writeback call. Backends use it to pick the right prompt-wrap
// template so the subagent gets a framing that matches the operation
// rather than a generic "here is a task" blob.
type SpawnPurpose string

const (
	// SpawnPurposeRetrieve is the pf_fault path: the subagent should
	// search the user's memory for information that answers a query.
	SpawnPurposeRetrieve SpawnPurpose = "retrieve"
	// SpawnPurposeWrite is the pf_poke mode:"agent" path: the subagent
	// should persist provided content into the user's memory at the
	// appropriate location.
	SpawnPurposeWrite SpawnPurpose = "write"
)

// SpawnRequest collects every input a SubagentBackend needs to run
// one agent turn. It replaces the old positional Spawn signature so
// new fields (purpose, time range, target hint, spawn id) can be
// added without churn at every call site.
//
// Task carries the *raw* caller content — the user's query for
// retrieve, or the content to persist for write. The backend is
// responsible for wrapping Task with the resolved prompt template
// before passing it through to its own command/body substitution.
type SpawnRequest struct {
	// AgentID picks which configured agent to run. Empty means "use
	// the backend's default agent" (typically the first configured).
	AgentID string
	// Task is the raw caller content. Retrieve: the query. Write: the
	// content to persist. The backend wraps this with its prompt
	// template before dispatch.
	Task string
	// Purpose selects the retrieve vs write prompt template. Empty
	// defaults to SpawnPurposeRetrieve, which matches the original
	// pf_fault-only call pattern.
	Purpose SpawnPurpose
	// TimeRange is an optional free-form hint restricting the
	// subagent's search to a time window. Interpreted verbatim by
	// the template via the {time_range} placeholder; empty means
	// "no restriction" and the template typically omits the line.
	TimeRange string
	// Target is an optional free-form hint for write calls, naming
	// where the subagent should prefer to place the content
	// ("daily", "long-term", "auto", etc.). Ignored for retrieve.
	Target string
	// SpawnID is a cryptographically random pf_sp_* token generated
	// by the dispatcher per call. Exposed to backend command /
	// HTTP-body templates as the {spawn_id} placeholder so external
	// agent runtimes (openclaw's gateway, etc.) can use it as an
	// isolated session key — by default an openclaw CLI run fixes
	// the session to agent:main:main and every pf_fault call
	// pollutes that shared session. Operators who wire {spawn_id}
	// into their command (e.g. `openclaw ... --session-id
	// {spawn_id}`) get one fresh session per call and no cross-
	// call context bleed. When the operator does not include
	// {spawn_id} in the template the value is silently ignored,
	// so the placeholder is backwards compatible with 0.9.x
	// configs.
	SpawnID string
	// Timeout caps the agent turn. Zero means "use the backend's
	// configured default".
	Timeout time.Duration
}

// DefaultRetrievePromptTemplate frames a subagent as a memory-recall
// specialist. This is what an agent sees on pf_fault when the
// operator has not configured a custom prompt template. The text is
// deliberately prescriptive — a bare "{task}" passthrough lets the
// subagent drift into generic Q&A behaviour (answering from its own
// world knowledge rather than the user's memory), which is the
// single most common failure mode reported from real deployments.
const DefaultRetrievePromptTemplate = `You are a memory-retrieval agent for the user's personal knowledge store. Your job is to SEARCH the user's memory for information that answers the query below — not to generate new content, and not to answer from your own training data or world knowledge.

Use every memory source available to you in this environment:
  - Index files (MEMORY.md, index.md, TOC.md, and similar)
  - Managed memory directories (workspace/memory, notes/, journals/, etc.)
  - Embedded search mechanisms (grep, ripgrep, code-search, qmd, etc.)
  - Structured memory databases (sqlite, vector stores, lossless-lcm, etc.)
  - Any other memory tool or service accessible in this environment

Ground your answer in what you actually find. Quote source paths and timestamps where possible. If you cannot find anything relevant, say so plainly and stop — do not invent content to look helpful, and do not fall back to your own general knowledge.

QUERY:
{task}
{time_range}`

// DefaultWritePromptTemplate frames a subagent as a memory-placement
// specialist for pf_poke mode:"agent". The subagent's job is to
// persist the content at the most appropriate location given the
// existing memory layout and conventions — not to judge whether to
// save it (the tool call has already decided "yes") and not to
// transform it beyond format adaptation.
const DefaultWritePromptTemplate = `You are a memory-write agent for the user's personal knowledge store. Your job is to PERSIST the content below into the user's memory at the most appropriate location — not to judge whether the content should be saved (the caller has already decided it should) and not to transform the content beyond what is needed for the target format.

Before writing, inspect the existing memory layout:
  - Read index files (MEMORY.md, index.md, TOC.md) to understand the directory conventions
  - Browse managed memory directories (workspace/memory, notes/, journals/) to learn the naming scheme and entry format
  - Prefer extending an existing file over creating a new one when the content is thematically related

When creating a new file, match the existing naming convention (e.g. YYYY-MM-DD.md for daily notes, topic-kebab-case.md for evergreen notes). When extending an existing file, match its existing entry format (headers, timestamps, separators, tags).

Report back the file path(s) you wrote to and a one-line summary of what you did.

TARGET HINT: {target}

CONTENT TO PERSIST:
{task}`

// ResolvePromptTemplate returns the effective prompt template for a
// Spawn call, applying the documented precedence:
//
//  1. Per-agent override (agentTmpl)
//  2. Per-backend default (backendTmpl)
//  3. Built-in default for the requested purpose
//
// Empty strings at any level fall through to the next step. An
// unknown purpose falls through to SpawnPurposeRetrieve's default so
// new call sites cannot accidentally produce an empty prompt.
func ResolvePromptTemplate(agentTmpl, backendTmpl string, purpose SpawnPurpose) string {
	if agentTmpl != "" {
		return agentTmpl
	}
	if backendTmpl != "" {
		return backendTmpl
	}
	switch purpose {
	case SpawnPurposeWrite:
		return DefaultWritePromptTemplate
	default:
		return DefaultRetrievePromptTemplate
	}
}

// WrapTask substitutes the SpawnRequest fields into tmpl. Placeholders:
//
//	{task}        — req.Task (always substituted)
//	{time_range}  — formatted time-range line, or empty
//	{target}      — req.Target, or empty
//	{agent_id}    — req.AgentID, or empty
//
// Unknown placeholders are left untouched. An empty template echoes
// the task unchanged so a misconfigured backend still returns
// something useful rather than a blank prompt.
//
// The time_range substitution is formatted as a standalone line
// beginning with "TIME RANGE:" when non-empty, so the template can
// slot it in with a leading blank line (`\n{time_range}`) and have
// the line collapse cleanly to nothing when no range is set.
func WrapTask(tmpl string, req SpawnRequest) string {
	if tmpl == "" {
		return req.Task
	}
	timeRangeLine := ""
	if req.TimeRange != "" {
		timeRangeLine = "\nTIME RANGE: " + req.TimeRange + "\nOnly consider memory content dated within this range; ignore matches outside it."
	}
	result := tmpl
	result = strings.ReplaceAll(result, "{task}", req.Task)
	result = strings.ReplaceAll(result, "{time_range}", timeRangeLine)
	result = strings.ReplaceAll(result, "{target}", req.Target)
	result = strings.ReplaceAll(result, "{agent_id}", req.AgentID)
	return result
}

// agentPromptOverride returns the per-agent template override for
// the given purpose if one exists. Helper used by both subagent
// backend implementations so the precedence rule lives in exactly
// one place.
//
// agents is typed as a map so backends can look up by id without
// a linear scan — the caller builds it once at construction time.
func agentPromptOverride(agents map[string]agentTemplates, agentID string, purpose SpawnPurpose) string {
	spec, ok := agents[agentID]
	if !ok {
		return ""
	}
	switch purpose {
	case SpawnPurposeWrite:
		return spec.WritePromptTemplate
	default:
		return spec.RetrievePromptTemplate
	}
}

// agentTemplates is the per-agent template record stored by the
// subagent backends. Split out of backend-specific config structs so
// the shared helpers can work against one common shape.
type agentTemplates struct {
	RetrievePromptTemplate string
	WritePromptTemplate    string
}
