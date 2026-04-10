package tool

import (
	"context"
	"fmt"

	"github.com/jet/pagefault/internal/backend"
	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/model"
)

// ReadInput is the request shape for read.
type ReadInput struct {
	URI      string `json:"uri"`
	FromLine int    `json:"from_line,omitempty"`
	ToLine   int    `json:"to_line,omitempty"`
}

// ReadOutput is the response shape for read.
type ReadOutput struct {
	Resource *backend.Resource `json:"resource"`
}

// HandleRead reads a resource by URI.
func HandleRead(ctx context.Context, d *dispatcher.ToolDispatcher, in ReadInput, caller model.Caller) (ReadOutput, error) {
	if in.URI == "" {
		return ReadOutput{}, fmt.Errorf("%w: uri is required", model.ErrInvalidRequest)
	}
	res, err := d.Read(ctx, in.URI, in.FromLine, in.ToLine, caller)
	if err != nil {
		return ReadOutput{}, err
	}
	return ReadOutput{Resource: res}, nil
}
