package backend

import (
	"context"
)

// AgentInfo describes a single subagent that a SubagentBackend can spawn.
type AgentInfo struct {
	// ID is the agent identifier used by pf_fault to pick which agent to
	// spawn. Must be unique within a backend.
	ID string `json:"id"`
	// Description is a human-readable summary of what the agent does. Shown
	// by pf_ps to help clients pick an agent.
	Description string `json:"description"`
}

// SubagentBackend is an extension of Backend that can spawn an external
// agent process to carry out a natural-language task. It is the substrate
// behind pf_fault (the "real" page fault: hand the miss to a smart worker
// and wait for the answer) and behind pf_poke mode:"agent" (delegate
// placement of a write to a subagent).
//
// A SubagentBackend implementation is still a regular Backend — its
// Read/Search/ListResources behaviour is backend-specific (typically noop
// or "list configured agents").
type SubagentBackend interface {
	Backend

	// Spawn runs the requested agent with the given SpawnRequest and
	// returns the agent's final textual response. The backend wraps
	// req.Task with the resolved prompt template (per-agent override →
	// per-backend default → built-in for the purpose) before
	// substituting it into its own command/body template, so callers
	// pass only the raw user content in req.Task and the framing is
	// applied consistently across every transport.
	//
	// The timeout is enforced by the backend (via ctx or an internal
	// clock, implementation's choice) — if the agent does not finish
	// in time, Spawn returns a wrapped model.ErrSubagentTimeout and
	// any partially-captured output as the string result.
	//
	// req.AgentID may be empty, in which case the backend picks its
	// default (typically the first configured agent). req.Purpose
	// defaults to SpawnPurposeRetrieve when empty so older call sites
	// that only cared about pf_fault semantics keep working without
	// explicit purpose annotation.
	Spawn(ctx context.Context, req SpawnRequest) (string, error)

	// ListAgents returns every agent configured on this backend. It is
	// zero-cost: backends populate this from config, no I/O.
	ListAgents() []AgentInfo
}

// hasAgentID reports whether id appears in agents. Shared by
// SubagentCLIBackend and SubagentHTTPBackend, which both validate the
// requested agent id on Spawn before doing any work.
func hasAgentID(agents []AgentInfo, id string) bool {
	for _, a := range agents {
		if a.ID == id {
			return true
		}
	}
	return false
}
