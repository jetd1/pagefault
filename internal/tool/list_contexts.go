package tool

import (
	"context"

	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/model"
)

// ListContextsInput has no fields; it is included for symmetry with the
// other tools and to give the MCP layer something to decode into.
type ListContextsInput struct{}

// ListContextsOutput is the response shape for list_contexts.
type ListContextsOutput struct {
	Contexts []dispatcher.ContextSummary `json:"contexts"`
}

// HandleListContexts returns every configured context. It is a transport-
// agnostic handler called from both the REST and MCP layers.
func HandleListContexts(ctx context.Context, d *dispatcher.ToolDispatcher, _ ListContextsInput, caller model.Caller) (ListContextsOutput, error) {
	sums, err := d.ListContexts(ctx, caller)
	if err != nil {
		return ListContextsOutput{}, err
	}
	return ListContextsOutput{Contexts: sums}, nil
}
