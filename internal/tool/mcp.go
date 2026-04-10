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
//
// Phase-1 wire names: pf_maps, pf_load, pf_scan, pf_peek. Internal Go names
// retain their generic form (HandleListContexts, etc.) — see CLAUDE.md for
// the wire-name ↔ code-name mapping.
func RegisterMCP(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	if d.ToolEnabled("pf_maps") {
		registerListContexts(srv, d)
	}
	if d.ToolEnabled("pf_load") {
		registerGetContext(srv, d)
	}
	if d.ToolEnabled("pf_scan") {
		registerSearch(srv, d)
	}
	if d.ToolEnabled("pf_peek") {
		registerRead(srv, d)
	}
}

// registerListContexts wires the pf_maps tool.
func registerListContexts(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	t := mcppkg.NewTool("pf_maps",
		mcppkg.WithDescription("List the memory regions (contexts) pre-mapped by this pagefault server. Returns each region's name and description. Use pf_load to fetch a region's content."),
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

// registerGetContext wires the pf_load tool.
func registerGetContext(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	t := mcppkg.NewTool("pf_load",
		mcppkg.WithDescription("Load a named memory region (context) into working memory. Assembles the region from its configured sources, applies filters, and returns the concatenated content."),
		mcppkg.WithString("name",
			mcppkg.Description("The region name (see pf_maps)"),
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

// registerSearch wires the pf_scan tool.
func registerSearch(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	t := mcppkg.NewTool("pf_scan",
		mcppkg.WithDescription("Scan configured backends for content matching a query. Returns ranked results with snippets."),
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

// registerRead wires the pf_peek tool.
func registerRead(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	t := mcppkg.NewTool("pf_peek",
		mcppkg.WithDescription("Peek at a specific resource by URI. Returns the resource content with optional line-range slicing for text resources."),
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
