// Package backend defines the interface that all pagefault data sources
// implement, along with the concrete backend implementations (filesystem,
// subprocess, http, subagent).
//
// Backends are the boundary between pagefault's generic tool surface and the
// actual data behind each source. A backend is responsible for:
//
//   - Resolving URIs to content (Read)
//   - Finding content matching a query (Search)
//   - Enumerating accessible resources (ListResources)
//
// Phase 1 ships a single backend type: filesystem. Phase 2 adds subprocess,
// http, and subagent backends.
package backend

import "context"

// Resource is a single piece of content returned from a backend.
type Resource struct {
	// URI is the backend-scheme qualified identifier, e.g. "memory://foo.md".
	URI string `json:"uri"`
	// Content is the raw body (text or serialized).
	Content string `json:"content"`
	// ContentType is an IANA media type (e.g. "text/markdown").
	ContentType string `json:"content_type"`
	// Metadata holds backend-specific info (source backend name, tags,
	// mtime, size, etc.).
	Metadata map[string]any `json:"metadata,omitempty"`
}

// SearchResult is a single hit from a Search call.
type SearchResult struct {
	// URI is the matched resource identifier.
	URI string `json:"uri"`
	// Snippet is a short excerpt showing the match context.
	Snippet string `json:"snippet"`
	// Score is an optional relevance score (higher = better). nil for
	// backends that do not rank.
	Score *float64 `json:"score,omitempty"`
	// Metadata holds tags, backend name, line numbers, etc.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ResourceInfo is a lightweight description of a resource for list/enumerate
// operations.
type ResourceInfo struct {
	URI      string         `json:"uri"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Backend is the interface that all data source plugins implement.
//
// All methods accept a context.Context for cancellation and deadlines.
// Implementations must honor context cancellation promptly.
type Backend interface {
	// Name returns the unique backend name from config. Used for routing
	// and audit logging.
	Name() string

	// Read fetches a single resource by URI. Returns ErrResourceNotFound if
	// the URI cannot be resolved.
	Read(ctx context.Context, uri string) (*Resource, error)

	// Search runs a query against the backend and returns up to limit
	// results. Backends that do not support search should return an empty
	// slice without error.
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)

	// ListResources enumerates accessible resources on this backend. Used
	// for the list/discovery tools and context source resolution.
	ListResources(ctx context.Context) ([]ResourceInfo, error)
}

// HealthChecker is an optional interface that backends may implement to
// signal liveness to the /health endpoint. Backends that do not
// implement it are reported as "ok" — only backends with a non-nil
// Health method get real probing. Implementations should honor the
// passed context (which carries the probe deadline) and return a
// short, cheap error on failure; /health summarises the first line
// of the error in its response.
type HealthChecker interface {
	Health(ctx context.Context) error
}
