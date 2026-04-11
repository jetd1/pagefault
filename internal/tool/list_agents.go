package tool

import (
	"context"

	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/model"
)

// ListAgentsInput is the request shape for pf_ps.
//
// 0.10.0 adds an optional TaskID field — when set, pf_ps switches
// from "list configured agents" mode to "poll an in-flight / recently
// completed pf_fault task" mode. The two modes are exposed on the
// same wire name because they answer adjacent questions ("what's
// running"): Cha's original design note for the async pf_fault
// polling pattern.
type ListAgentsInput struct {
	// TaskID, when set, flips pf_ps into task-polling mode. The
	// handler returns the task snapshot (status, answer,
	// elapsed, …) instead of the agent list. Unknown ids return
	// resource_not_found. Leave empty for the default agent-list
	// behaviour.
	TaskID string `json:"task_id,omitempty"`
}

// ListAgentsOutput is the response shape for pf_ps in list mode.
type ListAgentsOutput struct {
	Agents []dispatcher.AgentSummary `json:"agents"`
}

// HandleListAgents returns every agent exposed by every configured
// SubagentBackend when TaskID is empty, or the snapshot of a named
// task when TaskID is set. It is the pure, transport-agnostic body
// of pf_ps.
//
// The two modes return different shapes intentionally — agent lists
// are a slice and task snapshots are a single object, and mashing
// them into a single union would force every caller to pattern-match
// on shape. The server adapter layer routes to whichever shape the
// input selected.
func HandleListAgents(ctx context.Context, d *dispatcher.ToolDispatcher, in ListAgentsInput, caller model.Caller) (ListAgentsOutput, error) {
	agents, err := d.ListAgents(ctx, caller)
	if err != nil {
		return ListAgentsOutput{}, err
	}
	// Preserve a non-nil empty slice in JSON output — clients expect
	// "agents": [] rather than "agents": null.
	if agents == nil {
		agents = []dispatcher.AgentSummary{}
	}
	return ListAgentsOutput{Agents: agents}, nil
}

// HandleTaskStatus returns the snapshot of a single pf_fault task,
// used by pf_ps when TaskID is set. Shape mirrors DeepRetrieveOutput
// so callers can feed either through the same decoder.
//
// The handler is separate from HandleListAgents because the output
// shapes do not overlap — mashing them through one signature would
// force an any-typed return value that loses the JSON schema at
// the MCP layer. Having two exported symbols keeps the shapes
// typed.
func HandleTaskStatus(ctx context.Context, d *dispatcher.ToolDispatcher, in ListAgentsInput, caller model.Caller) (DeepRetrieveOutput, error) {
	res, err := d.GetTask(ctx, in.TaskID, caller)
	if err != nil {
		return DeepRetrieveOutput{}, err
	}
	return DeepRetrieveOutput{
		TaskID:         res.TaskID,
		Status:         res.Status,
		Agent:          res.Agent,
		Backend:        res.Backend,
		SpawnID:        res.SpawnID,
		Answer:         res.Answer,
		ElapsedSeconds: res.ElapsedSeconds,
		TimedOut:       res.TimedOut,
		PartialResult:  res.PartialResult,
		Error:          res.Error,
	}, nil
}
