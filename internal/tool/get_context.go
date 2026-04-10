package tool

import (
	"context"
	"fmt"

	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/model"
)

// GetContextInput is the request shape for get_context.
type GetContextInput struct {
	Name   string `json:"name"`
	Format string `json:"format,omitempty"`
}

// GetContextOutput is the response shape for get_context.
type GetContextOutput struct {
	Name    string `json:"name"`
	Format  string `json:"format"`
	Content string `json:"content"`
}

// HandleGetContext loads a named context from the dispatcher.
func HandleGetContext(ctx context.Context, d *dispatcher.ToolDispatcher, in GetContextInput, caller model.Caller) (GetContextOutput, error) {
	if in.Name == "" {
		return GetContextOutput{}, fmt.Errorf("%w: name is required", model.ErrInvalidRequest)
	}
	content, err := d.GetContext(ctx, in.Name, in.Format, caller)
	if err != nil {
		return GetContextOutput{}, err
	}
	format := in.Format
	if format == "" {
		format = "markdown"
	}
	return GetContextOutput{
		Name:    in.Name,
		Format:  format,
		Content: content,
	}, nil
}
