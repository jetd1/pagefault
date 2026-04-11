package tool

import (
	"context"
	"fmt"

	"jetd.one/pagefault/internal/dispatcher"
	"jetd.one/pagefault/internal/model"
)

// GetContextInput is the request shape for get_context.
type GetContextInput struct {
	Name   string `json:"name"`
	Format string `json:"format,omitempty"`
}

// GetContextOutput is the response shape for get_context. SkippedSources is
// populated when one or more configured sources were omitted (e.g., blocked
// by a filter or unreadable) so the caller can tell the bundle is partial.
type GetContextOutput struct {
	Name           string                     `json:"name"`
	Format         string                     `json:"format"`
	Content        string                     `json:"content"`
	SkippedSources []dispatcher.SkippedSource `json:"skipped_sources,omitempty"`
}

// HandleGetContext loads a named context from the dispatcher.
func HandleGetContext(ctx context.Context, d *dispatcher.ToolDispatcher, in GetContextInput, caller model.Caller) (GetContextOutput, error) {
	if in.Name == "" {
		return GetContextOutput{}, fmt.Errorf("%w: name is required", model.ErrInvalidRequest)
	}
	// The dispatcher returns the *resolved* format (after applying the
	// in.Format → context default → "markdown" fallback chain). Echo that
	// back so clients who let the context default kick in can still tell
	// which format their content is actually in.
	content, format, skipped, err := d.GetContext(ctx, in.Name, in.Format, caller)
	if err != nil {
		return GetContextOutput{}, err
	}
	return GetContextOutput{
		Name:           in.Name,
		Format:         format,
		Content:        content,
		SkippedSources: skipped,
	}, nil
}
