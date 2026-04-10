package tool

import (
	"context"

	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/model"
)

// ListAgentsInput is empty — pf_ps takes no parameters.
type ListAgentsInput struct{}

// ListAgentsOutput is the response shape for pf_ps.
type ListAgentsOutput struct {
	Agents []dispatcher.AgentSummary `json:"agents"`
}

// HandleListAgents returns every agent exposed by every configured
// SubagentBackend. It is the pure, transport-agnostic body of pf_ps.
func HandleListAgents(ctx context.Context, d *dispatcher.ToolDispatcher, _ ListAgentsInput, caller model.Caller) (ListAgentsOutput, error) {
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
