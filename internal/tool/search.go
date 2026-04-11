package tool

import (
	"context"
	"fmt"

	"jetd.one/pagefault/internal/dispatcher"
	"jetd.one/pagefault/internal/model"
)

// SearchInput is the request shape for search.
type SearchInput struct {
	Query    string   `json:"query"`
	Limit    int      `json:"limit,omitempty"`
	Backends []string `json:"backends,omitempty"`
	// DateRange is accepted for forward compatibility; Phase-1 backends
	// ignore it, but the field is preserved in the API surface.
	DateRange *DateRange `json:"date_range,omitempty"`
}

// DateRange is an inclusive date range hint for backends that support it.
type DateRange struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// SearchOutput is the response shape for search.
type SearchOutput struct {
	Results []dispatcher.SearchResult `json:"results"`
}

// HandleSearch runs a query across one or more backends via the dispatcher.
func HandleSearch(ctx context.Context, d *dispatcher.ToolDispatcher, in SearchInput, caller model.Caller) (SearchOutput, error) {
	if in.Query == "" {
		return SearchOutput{}, fmt.Errorf("%w: query is required", model.ErrInvalidRequest)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	results, err := d.Search(ctx, in.Query, limit, in.Backends, caller)
	if err != nil {
		return SearchOutput{}, err
	}
	return SearchOutput{Results: results}, nil
}
