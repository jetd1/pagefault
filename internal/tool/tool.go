// Package tool provides the Phase-1 tool handlers for pagefault. Each tool
// exposes:
//
//   - A typed Input/Output struct (JSON-tagged) used by both transports.
//   - A pure Handle* function that validates input, calls the dispatcher,
//     and returns the output. These functions are transport-agnostic.
//
// The server package wraps these handlers for the REST transport. The
// RegisterMCP function registers them with an mcp-go MCPServer.
package tool

import (
	"encoding/json"
	"fmt"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// toolResultJSON marshals a Go value to a CallToolResult containing a single
// text block with the JSON-encoded payload. This is the idiomatic way to
// return structured data from an mcp-go tool handler.
func toolResultJSON(v any) (*mcppkg.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("tool: marshal result: %w", err)
	}
	return &mcppkg.CallToolResult{
		Content: []mcppkg.Content{
			mcppkg.TextContent{Type: "text", Text: string(data)},
		},
	}, nil
}

// toolResultError builds a CallToolResult representing an error. mcp-go
// differentiates protocol errors from tool-level errors: tool errors are
// returned as a CallToolResult with IsError=true, not as an error value.
func toolResultError(err error) *mcppkg.CallToolResult {
	return &mcppkg.CallToolResult{
		IsError: true,
		Content: []mcppkg.Content{
			mcppkg.TextContent{Type: "text", Text: err.Error()},
		},
	}
}
