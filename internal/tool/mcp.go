package tool

import (
	"context"
	"encoding/json"
	"fmt"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"jetd.one/pagefault/internal/auth"
	"jetd.one/pagefault/internal/dispatcher"
)

// RegisterMCP registers every enabled tool on the given MCP server. The
// dispatcher uses the per-request Caller for filters and audit.
//
// Wire names: pf_maps, pf_load, pf_scan, pf_peek (Phase 1); pf_fault,
// pf_ps (Phase 2); pf_poke (Phase 4). Internal Go names retain their
// generic form (HandleListContexts, etc.) — see CLAUDE.md for the
// wire ↔ code map.
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
	if d.ToolEnabled("pf_fault") {
		registerDeepRetrieve(srv, d)
	}
	if d.ToolEnabled("pf_ps") {
		registerListAgents(srv, d)
	}
	if d.ToolEnabled("pf_poke") {
		registerWrite(srv, d)
	}
}

// brandingOpts returns the MCP tool annotation options that give
// every pf_* registration a consistent display title, correct
// read/destructive/idempotent hints, and the matching glyph icon
// from web/icons.svg. Centralised here so the hints for a given
// tool live in exactly one place and cannot drift between the
// tool registration and its documentation.
//
// mcp-go's NewTool defaults are DestructiveHint:true and
// IdempotentHint:false, which are wrong for the six read-only
// pf_* tools — without these overrides, Claude Desktop and every
// other MCP client would surface a "destructive" warning on a
// pf_scan or pf_peek call. The hint trio here correctly encodes
// "read memory, no side effects" for the read tools and keeps
// "writes the user's memory store" accurate for pf_poke.
func brandingOpts(wireName, title string, readOnly, destructive, idempotent bool) []mcppkg.ToolOption {
	return []mcppkg.ToolOption{
		mcppkg.WithTitleAnnotation(title),
		mcppkg.WithReadOnlyHintAnnotation(readOnly),
		mcppkg.WithDestructiveHintAnnotation(destructive),
		mcppkg.WithIdempotentHintAnnotation(idempotent),
		iconOptionFor(wireName),
	}
}

// registerListContexts wires the pf_maps tool.
func registerListContexts(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	opts := []mcppkg.ToolOption{
		mcppkg.WithDescription("List the memory regions (contexts) pre-mapped by this pagefault server. Call this first in a session to discover what personal-knowledge bundles are available (daily notes, projects, journals, etc.). Cheap; returns each region's name and description. Follow up with pf_load to fetch a region's content."),
	}
	opts = append(opts, brandingOpts("pf_maps", "List Memory Regions", true, false, true)...)
	t := mcppkg.NewTool("pf_maps", opts...)
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
	opts := []mcppkg.ToolOption{
		mcppkg.WithDescription("Load a whole named memory region (context) into working memory. Use when the user asks to pull up a whole bundle (\"load my daily notes for this week\", \"show me my project-X doc\") or when you already know the region name from pf_maps. Assembles the region from its configured sources, applies filters, and returns the concatenated content."),
		mcppkg.WithString("name",
			mcppkg.Description("The region name exactly as listed by pf_maps. Case-sensitive; whitespace matters. If you do not know what regions exist, call pf_maps first — guessing a name returns context_not_found and wastes a round-trip."),
			mcppkg.Required(),
		),
		mcppkg.WithString("format",
			mcppkg.Description("Output format. \"markdown\" (default) concatenates the region's sources into a single markdown document; use this for almost everything. \"markdown-with-metadata\" prepends each source with a YAML front-matter block carrying URI / backend / tags — useful when you need to cite where a passage came from. \"json\" returns a structured envelope ({sources: [{uri, content, metadata}]}) for programmatic consumption."),
		),
	}
	opts = append(opts, brandingOpts("pf_load", "Load Memory Region", true, false, true)...)
	t := mcppkg.NewTool("pf_load", opts...)
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
	opts := []mcppkg.ToolOption{
		mcppkg.WithDescription("Search the user's personal memory store for content matching a query. This is the right default for keyword/phrase lookups into notes, journals, past decisions, and project docs — reach for it whenever the user asks \"where did I write about X\" or \"what did I note about X\". Returns ranked snippets across every configured backend; follow up with pf_peek to read a specific hit in full."),
		mcppkg.WithString("query",
			mcppkg.Description("What to search for. Most pagefault backends are keyword/substring engines (grep, ripgrep, filesystem include/exclude), so short distinctive phrases work best: 2-6 tokens, prefer concrete entity names (project names, filenames, person names, dates) over abstract concepts. Examples: \"pagefault SSE transport\", \"auth middleware 2026-Q1\", \"wocha memory retrieval\". For natural-language questions you would phrase to a human (\"what did I decide about X\"), prefer pf_fault — pf_scan is a grep, not a semantic search. Split distinct questions into separate pf_scan calls rather than stuffing them into one query."),
			mcppkg.Required(),
		),
		mcppkg.WithNumber("limit",
			mcppkg.Description("Maximum number of results to return across all backends. Default 10, which is enough for most lookups. Raise to 30-50 when the query is deliberately broad (\"every note mentioning X\") and you want to see the long tail; lower to 3-5 when you only need the single best hit and want to minimise payload size."),
		),
		mcppkg.WithArray("backends",
			mcppkg.Description("Restrict the scan to specific backend names (as listed by pf_maps' source entries or the health probe). Leave empty — the default — to search every configured backend. Useful when you know the content you want lives in a specific place (e.g. set to [\"journal\"] when the user asks about daily notes) and scanning the rest would add noise."),
		),
	}
	opts = append(opts, brandingOpts("pf_scan", "Search Memory", true, false, true)...)
	t := mcppkg.NewTool("pf_scan", opts...)
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
	opts := []mcppkg.ToolOption{
		mcppkg.WithDescription("Read one specific resource from the memory store by URI (e.g. memory://memory/2026-04-11.md). Use after pf_scan surfaces a hit you want to read in full, or when the user names a path directly. Supports optional line-range slicing for large text resources."),
		mcppkg.WithString("uri",
			mcppkg.Description("Full resource URI including scheme. Filesystem backends use the operator-configured scheme (commonly memory://), with a path relative to the backend root: memory://MEMORY.md, memory://memory/2026-04-11.md, memory://memory/projects/design.md. Copy the URI verbatim from a pf_scan hit rather than reconstructing it — custom schemes (like notes:// or archive://) are common. Trailing whitespace or an accidental leading slash breaks the lookup."),
			mcppkg.Required(),
		),
		mcppkg.WithNumber("from_line",
			mcppkg.Description("Optional: first line (1-indexed, inclusive) of a range to return. Use with to_line for large files where you only need a specific section — avoids pulling an entire journal file when the user asked about a single paragraph. Omit to get the whole resource."),
		),
		mcppkg.WithNumber("to_line",
			mcppkg.Description("Optional: last line (1-indexed, inclusive) of a range to return. Use with from_line. A missing to_line combined with from_line streams from from_line to end of file."),
		),
	}
	opts = append(opts, brandingOpts("pf_peek", "Read Resource", true, false, true)...)
	t := mcppkg.NewTool("pf_peek", opts...)
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

// registerDeepRetrieve wires the pf_fault tool.
func registerDeepRetrieve(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	opts := []mcppkg.ToolOption{
		mcppkg.WithDescription("Trigger a page fault — spawn a subagent that reasons over the user's memory store and returns a synthesised answer. This is the heaviest and slowest tool in pagefault: the agent has its own tools and can make several lookups before replying. **Use sparingly** — only when pf_scan misses and the question genuinely needs intelligent retrieval (cross-reference, summarisation, fuzzy semantic lookup).\n\n**Async by default (0.10.0+).** This call returns immediately with `{task_id, status: \"running\"}` and the subagent runs on a background goroutine that survives HTTP disconnects. You MUST poll for the result: call `pf_ps` with `task_id` set to the returned id every ~30 seconds; the answer is ready when `status` is `done`, `timed_out`, or `failed`. The canonical polling budget is **30 seconds × 6 (≈3 minutes)**; stop earlier on a terminal status, and raise the count only for large `timeout_seconds` values (≥240) where the subagent genuinely needs more wall-clock. Do NOT poll faster than 30s — the subagent is mid-work and pagefault does nothing useful between polls. Set `wait: true` to get the old synchronous behaviour (blocks until terminal, bounded by `timeout_seconds`); MCP clients should generally prefer async + poll because HTTP middleware timeouts kill long sync calls.\n\nThe subagent is automatically framed as a memory-retrieval specialist by the server-side prompt template, so you do not need to rephrase the query as a search instruction — a plain user question is correct input."),
		mcppkg.WithString("query",
			mcppkg.Description("The user's question in natural language, framed around what they want to recall. Examples: \"what did I note about the pagefault SSE fix?\", \"which projects touched the auth middleware last quarter?\", \"summarise every journal entry mentioning wocha\". Include concrete entity names, topics, and dates where the user supplied them — the subagent uses these to guide its search, so a vague query produces a vague answer. Do NOT rephrase into \"search for X in my memory\"; the server-side prompt template already tells the agent it is a memory-retrieval worker."),
			mcppkg.Required(),
		),
		mcppkg.WithString("agent",
			mcppkg.Description("Subagent id to spawn. **If pf_ps returns more than one agent, you MUST call pf_ps first (in list mode, no task_id) and pick by description** — common splits include work vs personal, short-term vs long-term, or journal vs project notes, and the \"first configured agent\" fallback will silently pick the wrong one. Only leave empty when pf_ps shows exactly one agent (single-agent configs). If the user's question straddles scopes, make two pf_fault calls with different agents and merge the results yourself."),
		),
		mcppkg.WithNumber("timeout_seconds",
			mcppkg.Description("Maximum seconds the subagent is allowed to run before the task is marked timed_out. Default 120, and that is already a minimum — real deep-retrieval runs typically take 20-40 seconds just to produce their first token and can exceed a minute when fanning out across multiple memory sources (MEMORY.md, daily notes, LCM/sqlite, etc.). **Do not set this below 120.** Raise to 180-300 for hard lookups (cross-source summarisation, long time ranges, whole-project recall). On timeout the poll eventually returns `status: \"timed_out\"` with whatever the agent produced in `partial_result`, not an error — but a too-short timeout truncates the run before the agent can even finish reading its sources, so prefer a longer budget over a shorter one. When you raise `timeout_seconds` above 180, raise your poll budget proportionally (e.g. 30s × 8 for 240s; 30s × 12 for 360s)."),
		),
		mcppkg.WithString("time_range_start",
			mcppkg.Description("Optional earliest date/time to include in the subagent's search. Free-form — any human-readable form works (ISO 8601 like \"2026-04-01\", \"last Tuesday\", \"Q1 2026\"). Pagefault does not parse the value; it is passed through to the subagent via the prompt template's {time_range} placeholder so the agent can interpret it in context. Pair with time_range_end to scope to a window, or leave time_range_end empty for \"from X onwards\"."),
		),
		mcppkg.WithString("time_range_end",
			mcppkg.Description("Optional latest date/time to include in the subagent's search. Same free-form rules as time_range_start — pagefault does not validate, the subagent interprets. Leave time_range_start empty for \"up to Y\"; set both for a closed window."),
		),
		mcppkg.WithBoolean("wait",
			mcppkg.Description("Synchronous compatibility flag. Default (false) is the 0.10.0 async path: returns immediately with `{task_id, status: \"running\"}` and you poll `pf_ps(task_id=...)` every 30 seconds (×6) for the result. Set to true to block the call until the subagent reaches a terminal state and return the full `{answer, elapsed_seconds, ...}` inline. Sync mode is bounded by `timeout_seconds` and is vulnerable to HTTP-level middleware timeouts (proxies, load balancers) that can kill long connections — use it only for short, deterministic calls (CLI scripts, tests). MCP agent clients should leave this unset and use the polling pattern."),
		),
	}
	// Read-only but not idempotent: pf_fault does not mutate user
	// memory, but each call spawns a fresh task record (TTL-tracked
	// state) so repeated calls are not effect-free.
	opts = append(opts, brandingOpts("pf_fault", "Deep Memory Query", true, false, false)...)
	t := mcppkg.NewTool("pf_fault", opts...)
	srv.AddTool(t, func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		args := req.GetArguments()
		in := DeepRetrieveInput{
			Query:          asString(args["query"]),
			Agent:          asString(args["agent"]),
			TimeoutSeconds: asInt(args["timeout_seconds"]),
			TimeRangeStart: asString(args["time_range_start"]),
			TimeRangeEnd:   asString(args["time_range_end"]),
			Wait:           asBool(args["wait"]),
		}
		caller := auth.CallerFromContext(ctx)
		out, err := HandleDeepRetrieve(ctx, d, in, caller)
		if err != nil {
			return toolResultError(err), nil
		}
		return toolResultJSON(out)
	})
}

// registerListAgents wires the pf_ps tool.
func registerListAgents(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	opts := []mcppkg.ToolOption{
		mcppkg.WithDescription("List configured subagents **or** poll a running pf_fault task — `pf_ps` is the `ps` of pagefault, answering the question \"what's happening with my agents\".\n\n**Mode A — list agents** (default, `task_id` empty): returns every configured subagent with id, description, and host backend. **Call this before pf_fault or pf_poke mode:agent whenever more than one agent is configured** — the descriptions are how you route a query to the right agent (work vs personal, short-term vs long-term, journal vs project notes). Cheap and local; no subagent spawn.\n\n**Mode B — poll a task** (`task_id` set): returns a snapshot of one pf_fault task with `{status, answer?, partial_result?, error?, elapsed_seconds, agent, backend}`. Status is `running` while the subagent is still working, and `done` / `timed_out` / `failed` once terminal. **Use this to poll pf_fault's async return**: after pf_fault gives you a task id, call `pf_ps` with `task_id` set every 30 seconds (up to 6 times for the default 120s pf_fault budget). Stop polling the moment `status` becomes terminal. Unknown or expired (TTL ~10min) task ids return resource_not_found."),
		mcppkg.WithString("task_id",
			mcppkg.Description("Task id returned by a previous pf_fault call (format `pf_tk_...`). When set, pf_ps returns the task snapshot instead of the agent list. Leave empty to list configured agents. Unknown or expired ids return resource_not_found — task snapshots are kept in memory for ~10 minutes after completion, so polling within that window is safe but long-gone tasks are gone."),
		),
	}
	opts = append(opts, brandingOpts("pf_ps", "Agent Status", true, false, true)...)
	t := mcppkg.NewTool("pf_ps", opts...)
	srv.AddTool(t, func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		args := req.GetArguments()
		in := ListAgentsInput{
			TaskID: asString(args["task_id"]),
		}
		caller := auth.CallerFromContext(ctx)
		if in.TaskID != "" {
			status, err := HandleTaskStatus(ctx, d, in, caller)
			if err != nil {
				return toolResultError(err), nil
			}
			return toolResultJSON(status)
		}
		out, err := HandleListAgents(ctx, d, in, caller)
		if err != nil {
			return toolResultError(err), nil
		}
		return toolResultJSON(out)
	})
}

// registerWrite wires the pf_poke tool. Two modes:
//
//   - mode:"direct" appends content to a URI (filesystem backend).
//     The backend enforces its own write_paths allowlist, write_mode,
//     and max_entry_size.
//   - mode:"agent" hands the content to a subagent that decides where
//     to persist it. Trust is delegated to the subagent — pagefault
//     does not re-validate what the agent writes.
func registerWrite(srv *mcpserver.MCPServer, d *dispatcher.ToolDispatcher) {
	opts := []mcppkg.ToolOption{
		mcppkg.WithDescription("Write content back into the user's memory store. Use when the user says \"remember this\", \"save this\", \"log this\", \"journal this\" — or when a reasoning step produces a durable insight worth persisting. mode:\"direct\" appends to a specific URI (filesystem backends only); mode:\"agent\" delegates placement to a subagent that decides where the content belongs. The write-side counterpart to pf_peek."),
		mcppkg.WithString("uri",
			mcppkg.Description("mode:direct only — target resource URI, including the full scheme, matching the operator's write_paths allowlist. Examples: memory://memory/2026-04-11.md, memory://memory/notes.md. Appending to an existing file is preferred over creating a new one; use a stable date-based name (YYYY-MM-DD) or topic-based slug so repeated pokes land in the same place. The server rejects URIs outside write_paths with 403 access_violation. Ignored in mode:agent — the subagent picks the target itself."),
		),
		mcppkg.WithString("content",
			mcppkg.Description("The text to persist, in markdown (the default wrapper is a markdown block) or whatever format the backend accepts. Include everything the user will want back later — context, not just the bare fact. Example good content: \"Fixed the GLM-5.1 subagent timeout by raising wocha's http.Client timeout from 15s to 60s. Change in backends/wocha/runner.go, Apr 11 2026.\" Example bad content: \"fixed it\". Keep each call under a few hundred tokens; for long content, summarise or use mode:agent so the subagent can split it across files."),
			mcppkg.Required(),
		),
		mcppkg.WithString("mode",
			mcppkg.Description("\"direct\" appends to a specific URI — fast, deterministic, zero LLM cost; the right choice when you already know where the content belongs. \"agent\" delegates placement to a subagent that inspects the memory layout and decides where to write — slower but handles novel content that does not fit an existing file. Default to direct when the caller provided a URI or a clear filename convention; fall back to agent for free-form recall (\"remember that I like vim\") where placement is non-obvious."),
			mcppkg.Required(),
		),
		mcppkg.WithString("format",
			mcppkg.Description("mode:direct only. \"entry\" (default) wraps the content as a timestamped markdown block — `\\n---\\n## [HH:MM] via pagefault (<caller>)\\n\\n<content>\\n` — so repeated pokes build a readable journal. \"raw\" appends the bytes unchanged; use when you are writing structured data (JSON lines, CSV) or when the caller has already formatted the content and the wrapper would corrupt it. Raw requires the backend to be write_mode:any; attempting raw on an append-only backend returns invalid_request."),
		),
		mcppkg.WithString("agent",
			mcppkg.Description("mode:agent only — subagent id to spawn. **If pf_ps returns more than one agent, you MUST call pf_ps first and pick by description** rather than relying on the \"first configured agent\" fallback. Placement matters here: writing a personal journal entry to a work-scoped agent (or vice versa) produces the wrong file layout and the wrong audience. Only leave empty when pf_ps shows exactly one agent."),
		),
		mcppkg.WithString("target",
			mcppkg.Description("mode:agent only — a free-form placement hint passed through to the subagent via the write prompt template's {target} placeholder. Common values: \"auto\" (default, let the agent decide), \"daily\" (append to today's journal), \"long-term\" (stable evergreen notes), or a topic name (\"auth\", \"pagefault\"). Empty defaults to \"auto\"."),
		),
		mcppkg.WithNumber("timeout_seconds",
			mcppkg.Description("mode:agent only — per-call deadline in seconds. Default 120, and that is already a minimum. A write-agent typically reads the existing memory layout before placing the content, then performs one or more file writes, so real runs take 30-60+ seconds end-to-end. **Do not set this below 120.** Raise to 180-300 for heavy write tasks that need to scan many existing files (deciding whether to extend or create) before writing. On timeout the tool returns timed_out:true with the subagent's partial output — but a too-short timeout cuts the agent off before it can finish placing the content, so prefer a longer budget over a shorter one."),
		),
	}
	opts = append(opts, brandingOpts("pf_poke", "Write to Memory", false, true, false)...)
	t := mcppkg.NewTool("pf_poke", opts...)
	srv.AddTool(t, func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		args := req.GetArguments()
		in := WriteInput{
			URI:            asString(args["uri"]),
			Content:        asString(args["content"]),
			Mode:           asString(args["mode"]),
			Format:         asString(args["format"]),
			Agent:          asString(args["agent"]),
			Target:         asString(args["target"]),
			TimeoutSeconds: asInt(args["timeout_seconds"]),
		}
		caller := auth.CallerFromContext(ctx)
		out, err := HandleWrite(ctx, d, in, caller)
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

// asBool coerces MCP tool argument values into bool. Accepts native
// bool, the strings "true"/"false" (case-insensitive), and treats
// any other shape — nil, empty string, number, etc. — as false.
func asBool(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		switch {
		case t == "true", t == "True", t == "TRUE":
			return true
		}
		return false
	default:
		return false
	}
}
