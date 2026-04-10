package tool

import (
	"context"
	"encoding/json"
	"fmt"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jet/pagefault/internal/auth"
	"github.com/jet/pagefault/internal/dispatcher"
)

// RegisterMCP registers every enabled Phase-1 tool on the given MCP server.
// The caller provides a helper to extract the pagefault Caller from the
// request context — the dispatcher uses this for filters and audit.
func RegisterMCP(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	if d.ToolEnabled("list_contexts") {
		registerListContexts(srv, d)
	}
	if d.ToolEnabled("get_context") {
		registerGetContext(srv, d)
	}
	if d.ToolEnabled("search") {
		registerSearch(srv, d)
	}
	if d.ToolEnabled("read") {
		registerRead(srv, d)
	}
}

func registerListContexts(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	t := mcppkg.NewTool("list_contexts",
		mcppkg.WithDescription("List all pre-composed memory contexts available on this pagefault server. Returns each context's name and description."),
	)
	srv.AddTool(t, func(ctx context.Context, _ mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		caller := auth.CallerFromContext(ctx)
		out, err := HandleListContexts(ctx, d, ListContextsInput{}, caller)
		if err != nil {
			return toolResultError(err), nil
		}
		return toolResultJSON(out)
	})
}

func registerGetContext(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	t := mcppkg.NewTool("get_context",
		mcppkg.WithDescription("Load and return a pre-composed context by name. Contexts are defined in the server config and bundle one or more backend sources."),
		mcppkg.WithString("name",
			mcppkg.Description("The context name (see list_contexts)"),
			mcppkg.Required(),
		),
		mcppkg.WithString("format",
			mcppkg.Description("Output format: 'markdown' (default) or 'json'"),
		),
	)
	srv.AddTool(t, func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		args := req.GetArguments()
		in := GetContextInput{
			Name:   asString(args["name"]),
			Format: asString(args["format"]),
		}
		caller := auth.CallerFromContext(ctx)
		out, err := HandleGetContext(ctx, d, in, caller)
		if err != nil {
			return toolResultError(err), nil
		}
		return toolResultJSON(out)
	})
}

func registerSearch(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	t := mcppkg.NewTool("search",
		mcppkg.WithDescription("Search across configured backends. Returns ranked results with snippets."),
		mcppkg.WithString("query",
			mcppkg.Description("Search query (keywords, phrase, or natural language)"),
			mcppkg.Required(),
		),
		mcppkg.WithNumber("limit",
			mcppkg.Description("Maximum number of results (default: 10)"),
		),
		mcppkg.WithArray("backends",
			mcppkg.Description("Restrict to specific backend names"),
		),
	)
	srv.AddTool(t, func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		args := req.GetArguments()
		in := SearchInput{
			Query:    asString(args["query"]),
			Limit:    asInt(args["limit"]),
			Backends: asStringSlice(args["backends"]),
		}
		caller := auth.CallerFromContext(ctx)
		out, err := HandleSearch(ctx, d, in, caller)
		if err != nil {
			return toolResultError(err), nil
		}
		return toolResultJSON(out)
	})
}

func registerRead(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	t := mcppkg.NewTool("read",
		mcppkg.WithDescription("Read a specific resource by URI. Supports optional line-range slicing for text resources."),
		mcppkg.WithString("uri",
			mcppkg.Description("Resource URI (e.g. memory://2026-04-10.md)"),
			mcppkg.Required(),
		),
		mcppkg.WithNumber("from_line",
			mcppkg.Description("Start line (1-indexed) for text resources"),
		),
		mcppkg.WithNumber("to_line",
			mcppkg.Description("End line (inclusive) for text resources"),
		),
	)
	srv.AddTool(t, func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		args := req.GetArguments()
		in := ReadInput{
			URI:      asString(args["uri"]),
			FromLine: asInt(args["from_line"]),
			ToLine:   asInt(args["to_line"]),
		}
		caller := auth.CallerFromContext(ctx)
		out, err := HandleRead(ctx, d, in, caller)
		if err != nil {
			return toolResultError(err), nil
		}
		return toolResultJSON(out)
	})
}

// ───────────────── coercion helpers ─────────────────
//
// MCP tool arguments arrive as map[string]any with JSON-decoded values:
// strings as string, numbers as float64, arrays as []any. These helpers
// coerce them into the types our handlers expect.

func asString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

func asInt(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		// Empty string maps to 0; other strings are ignored.
		return 0
	default:
		return 0
	}
}

func asStringSlice(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
