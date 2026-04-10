package backend

import (
	"context"
	"time"
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
// and wait for the answer).
//
// A SubagentBackend implementation is still a regular Backend — its
// Read/Search/ListResources behaviour is backend-specific (typically noop
// or "list configured agents").
type SubagentBackend interface {
	Backend

	// Spawn runs the agent identified by agentID with the given task and
	// returns the agent's final textual response. The timeout is enforced
	// by the backend (via ctx or an internal clock, implementation's
	// choice) — if the agent does not finish in time, Spawn returns a
	// wrapped model.ErrSubagentTimeout and any partially-captured output
	// as the string result.
	//
	// agentID may be empty, in which case the backend picks its default
	// (typically the first configured agent).
	Spawn(ctx context.Context, agentID string, task string, timeout time.Duration) (string, error)

	// ListAgents returns every agent configured on this backend. It is
	// zero-cost: backends populate this from config, no I/O.
	ListAgents() []AgentInfo
}
