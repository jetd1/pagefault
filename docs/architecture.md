# pagefault — Architecture

This document describes the runtime architecture of pagefault. It is a
condensed version of `plan.md` §3–5 and should match the code in
`internal/`. If it drifts, update this doc.

## The one-line description

pagefault is a **config-driven memory server** that exposes personal
knowledge (files, search indices, agent sessions) to external AI clients via
MCP (streamable-http *and* legacy SSE) and a REST / OpenAPI transport.

## Component overview

```
┌──────────────────────────────────────────────────┐
│  Clients (Claude Code, Claude Desktop, ChatGPT,  │
│  curl, etc.)                                      │
└─────┬────────────────┬───────────────┬──────────┘
      │ /mcp           │ /sse, /message│ /api/pf_*
      │ streamable     │ legacy SSE    │ REST
      ▼                ▼               ▼
┌──────────────────────────────────────────────────┐
│  chi router + middleware (Recoverer, Logger)      │
│  ┌──────────┐                                    │
│  │ auth     │  bearer / trusted_header / none    │
│  └────┬─────┘                                    │
│       │                                          │
│       ▼                                          │
│  ┌───────────────────────────────┐               │
│  │ Tool layer                    │               │
│  │  pf_maps / pf_load (P1)       │               │
│  │  pf_scan / pf_peek (P1)       │               │
│  │  pf_fault / pf_ps  (P2)       │               │
│  │  pf_poke           (P4)       │               │
│  └────────────┬──────────────────┘               │
│               │                                  │
│               ▼                                  │
│  ┌───────────────────────────────┐               │
│  │ ToolDispatcher                │               │
│  │  - filter.AllowURI  (pre)     │               │
│  │  - backend.Read/Search        │               │
│  │  - filter.AllowTags (post)    │               │
│  │  - filter.FilterContent       │               │
│  │  - audit.Log                  │               │
│  └────────────┬──────────────────┘               │
│               │                                  │
│   ┌───────────┴───────────┐                      │
│   ▼                       ▼                      │
│ Backend registry       Audit logger              │
│ (filesystem, subproc,  (JSONL / stdout / off)    │
│  http, subagent-cli,                             │
│  subagent-http)                                  │
└──────────────────────────────────────────────────┘
```

## Package map

| Package                 | Responsibility |
|-------------------------|----------------|
| `cmd/pagefault`         | CLI entry point: `serve`, `token`, `--version` |
| `internal/config`       | YAML schema structs, loader, env substitution, validation |
| `internal/model`        | Shared types (`Caller`) and sentinel errors |
| `internal/backend`      | `Backend` / `SubagentBackend` / `WritableBackend` interfaces and five built-in types: `filesystem` (P1, P4 write support), `subprocess`, `http`, `subagent-cli`, `subagent-http` (P2) |
| `internal/auth`         | `AuthProvider` interface, Bearer/None/TrustedHeader, middleware |
| `internal/filter`       | `Filter` interface, `CompositeFilter`, PathFilter (read + write allowlists), TagFilter, RedactionFilter (P3) |
| `internal/write`        | `Writer` interface + `FilesystemWriter` (flock + atomic append), entry-format templating — Phase 4 |
| `internal/audit`        | `Logger` interface, JSONL/stdout/nop sinks, arg sanitization |
| `internal/dispatcher`   | Central tool router: filter + backend + audit pipeline, markdown / markdown-with-metadata / json context formats, direct-write routing (P4) |
| `internal/tool`         | Per-tool Handle\* functions and MCP registrations (including `HandleWrite` for `pf_poke`) |
| `internal/server`       | chi router, MCP mount, REST adapter, health probes, CORS + rate limit middleware, OpenAPI spec |

## Request flow

1. **HTTP request arrives** at `/api/{tool}` or `/mcp`.
2. **Recoverer + Logger** middlewares run.
3. **Auth middleware** validates the credential (bearer token / trusted
   header / none) and injects a `model.Caller` into the request context.
4. **Transport adapter** (REST `restHandler[In,Out]` generic or mcp-go's
   tool handler) decodes the input into a typed struct.
5. **Tool handler** (`tool.HandleX`) validates the input and calls the
   dispatcher.
6. **Dispatcher** runs the filter pipeline, executes the backend operation,
   applies content transforms, writes the audit entry, and returns the
   result.
7. **Transport adapter** serializes the result to JSON.

## Filter pipeline

```
Read path:
caller → AllowURI  ──(block)──▶ 403 ErrAccessViolation
         │
         ▼
         backend.Read / Search
         │
         ▼
         AllowTags ──(block)──▶ 403 ErrAccessViolation
         │
         ▼
         FilterContent   (RedactionFilter; identity when disabled)
         │
         ▼
         audit.Log ──▶ response

Write path (pf_poke direct):
caller → AllowWriteURI ──(block)──▶ 403 ErrAccessViolation
         │
         ▼
         WritableBackend.Write
           ├── Writable() check
           ├── write_paths allowlist
           ├── max_entry_size cap
           └── resolveWritePath + flock + atomic append
         │
         ▼
         audit.Log ──▶ response
```

- **AllowURI** is called before the backend is touched. A denied URI never
  hits disk.
- **AllowTags** runs after the backend returns, with the resource's tag set
  (from `auto_tag` config rules on the filesystem backend).
- **FilterContent** runs `RedactionFilter` (Phase 3) when rules are
  configured; otherwise it is the identity function. The un-redacted
  copy never leaves the dispatcher.
- **AllowWriteURI** (Phase 4) is the mutation gate. It's checked
  *instead of* `AllowURI` on the write path — the PathFilter falls back
  to the read allow/deny pair when no write-specific globs are
  configured, so the simple case of "read == write" still works.
- **WritableBackend.Write** is a type assertion on the backend — if
  the backend does not implement it, or `Writable()` is false, the
  dispatcher returns `ErrAccessViolation` before any file-system call.

## Backend model

`internal/backend/backend.go` defines:

```go
type Backend interface {
    Name() string
    Read(ctx context.Context, uri string) (*Resource, error)
    Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
    ListResources(ctx context.Context) ([]ResourceInfo, error)
}
```

**FilesystemBackend** (Phase 1 + Phase 4 writes). Responsibilities:

- Map URIs (`memory://foo.md`) to filesystem paths under the configured root
- Enforce an include/exclude glob filter (doublestar syntax)
- Enforce a sandbox that rejects symlinks escaping the root (on
  reads via `EvalSymlinks`, on writes via `resolveWritePath` which
  walks the parent chain to find the first existing component)
- Auto-tag resources by path pattern
- Serve Read / Search / ListResources
- When `writable: true`, implement `WritableBackend.Write` — enforce
  `write_paths`, `write_mode`, and `max_entry_size`, then delegate
  the atomic append to `internal/write.FilesystemWriter`

Search is naive substring matching (case-insensitive, first match per file).
It is fast enough for thousands of small markdown files; a future phase can
add an index-backed backend type.

**Phase-2 backends** (shipped in 0.3.0):

- **SubprocessBackend** — runs an external command (canonical case:
  ripgrep) and parses stdout. Parse modes: `ripgrep_json`, `grep`
  (`path:lineno:content`), `plain`. Read is unsupported (use a
  filesystem backend alongside it if you need content). Exit code 1 is
  treated as "no matches" rather than an error.
- **HTTPBackend** — generic HTTP search backend. Issues a single HTTP
  request per `Search`, extracts a result array with a dotted
  `response_path`, and converts each element into a `SearchResult`.
- **SubagentCLIBackend** / **SubagentHTTPBackend** — implement
  `SubagentBackend`, which extends `Backend` with
  `Spawn(ctx, SpawnRequest)` and `ListAgents()`. `SpawnRequest`
  carries the agent id, the raw task, a `SpawnPurpose`
  (`retrieve` or `write`), an optional free-form `TimeRange`
  hint, an optional placement `Target` hint (write only), and a
  timeout — future additions (caller context, tool-call budgets,
  tracing ids) can land without another signature change.
  `pf_fault` calls `Spawn` with `Purpose=retrieve`; `pf_poke`
  mode:"agent" calls the dispatcher's new `DelegateWrite` method
  which in turn calls `Spawn` with `Purpose=write` so the backend
  picks the write-framed prompt template (see
  "Server-side prompt framing" below). `pf_ps` calls `ListAgents`
  across every configured `SubagentBackend`.

All backend constructors live in `cmd/pagefault/serve.go`'s
`buildDispatcher`, which is the single switch on `bc.Type` that wires
backends from YAML into the dispatcher.

### Subagent trust model

Subagents are external processes (or remote HTTP endpoints) that
pagefault *cannot* sandbox. The security perimeter of pagefault ends
at `SubagentBackend.Spawn` — everything the agent does after that
runs with the operator's privileges, not pagefault's. Concretely:

- The filter pipeline does *not* apply to what an agent reads or
  writes; it only applies to `pf_fault`'s *request* (the query and
  agent id), not to the agent's subsequent workspace access.
- Agents supplied by `subagent-cli` inherit the pagefault process's
  environment and file descriptors. Operators should pick a command
  template that runs in an appropriate sandbox if that matters.
- Agents supplied by `subagent-http` are trusted to enforce their own
  access control; pagefault just forwards the task.
- Timeouts are enforced by pagefault (`exec.CommandContext` kills the
  child; the HTTP client cancels the request) but a misbehaving agent
  can still complete side effects before the deadline fires.

### Server-side prompt framing

Before handing a task to a subagent, the subagent backend wraps the
raw caller content with a **resolved prompt template**. This is not
a security boundary — it's a behaviour lever. The problem it fixes:
a fresh subagent with no prior context will treat a `pf_fault`
query like a generic Q&A prompt and answer from its own training
data ("what did I note about oleander" → toxicity sheet instead of
chat history). The template tells the agent explicitly:
"you are a memory-retrieval agent, search the user's memory
sources (MEMORY.md, managed directories, qmd, lossless-lcm, …),
do not fall back to your training data".

The resolution chain is three layers, each overriding the next:

1. **Per-agent override** — `AgentSpec.retrieve_prompt_template`
   or `AgentSpec.write_prompt_template` on a specific agent entry
   in the YAML config.
2. **Per-backend default** — `retrieve_prompt_template` or
   `write_prompt_template` on the `Subagent*BackendConfig`.
3. **Built-in default** — `backend.DefaultRetrievePromptTemplate`
   or `backend.DefaultWritePromptTemplate`, selected by the
   `SpawnRequest.Purpose` field.

The resolved template is then run through `backend.WrapTask`,
which substitutes `{task}`, `{time_range}`, `{target}`, and
`{agent_id}` placeholders. Unknown placeholders pass through
unchanged — operators can add their own without source changes.
Empty time_range collapses its whole line so the template does
not emit a trailing "Time range:" header for calls that did not
set one.

The two default templates live in `internal/backend/prompt.go`
and encode pagefault's opinion about what a memory retriever /
placer should do:

- **Retrieve default** — "enumerate the user's memory sources,
  search them for the query, cite the sources in your answer,
  do not invent content if nothing is found".
- **Write default** — "read the current memory layout before
  placing the content, match the user's naming convention,
  extend existing files when themes overlap, report the
  path(s) written".

Because the template is applied by the backend, it is consistent
whether the subagent was invoked through `pf_fault` or through
`pf_poke` mode:"agent". The dispatcher's `DelegateWrite` method
exists specifically so `pf_poke` can route to `Spawn` with
`Purpose=write` and pick the write template, rather than tunneling
through `DeepRetrieve` which would use the retrieve template.

## Auth layer

Three providers, all implementing `AuthProvider.Authenticate(r) (*Caller, error)`:

- **NoneAuth** — always returns `AnonymousCaller`. Local dev only.
- **BearerTokenAuth** — loads a JSONL tokens file at startup, matches
  `Authorization: Bearer <tok>` against it. Supports `Reload()` to pick up
  changes without a restart.
- **TrustedHeaderAuth** — reads identity from a configurable header, with
  optional trusted-proxy IP allowlist. Intended for deployments behind a
  reverse proxy that handles auth externally.

`auth.Middleware` wraps any `AuthProvider` as an HTTP middleware that stores
the resolved `Caller` on the request context. Tool handlers retrieve it with
`auth.CallerFromContext(ctx)`.

## Audit

Every tool call is logged. Each entry has:

- `timestamp` (UTC, RFC3339)
- `caller_id`, `caller_label`
- `tool`
- `args` (with sensitive keys replaced by `[REDACTED]`)
- `duration_ms`
- `result_size`
- `error` (empty on success)

Three sinks: `JSONLLogger`, `StdoutLogger`, `NopLogger`. The jsonl logger
serializes writes through a mutex and is safe for concurrent use.

## Tool dispatch

`dispatcher.New(Options)` validates that every backend name is unique, that
every context references a known backend, and that every backend with a URI
scheme claims an unambiguous scheme.

The dispatcher owns:

- `backends`  — `map[name]Backend` plus ordered list
- `schemeToBackend` — for routing `read` calls
- `contexts`  — `map[name]ContextConfig`
- `filter`    — `*CompositeFilter`
- `auditLog`  — `audit.Logger`
- `toolsCfg`  — enable/disable flags

Tool handlers in `internal/tool` are thin wrappers over the dispatcher's
`ListContexts`, `GetContext`, `Search`, `Read`, `DeepRetrieve`,
`ListAgents`, `Write`, and `DelegateWrite` methods. `DelegateWrite`
is the write-side twin of `DeepRetrieve` — both spawn a subagent,
but `DelegateWrite` tags the `SpawnRequest` with
`Purpose=write` so the subagent picks up the write-framed prompt
template instead of the retrieve-framed one, and passes a free-form
`Target` hint ("daily", "long-term", "auto", …) through the
template's `{target}` placeholder.

## Transport details

### REST

- chi router with `/api` sub-router
- Auth middleware applied to the authenticated group
- `restHandler[In, Out]` generic adapter converts pure `tool.HandleX`
  functions into `http.HandlerFunc`s (JSON decode → caller extract → handler
  → error status mapping → JSON encode)

### MCP

- `mcpserver.NewMCPServer("pagefault", Version, WithToolCapabilities(true),
  WithInstructions(...))` builds the single shared `MCPServer`. The
  instructions argument defaults to
  `internal/tool.DefaultInstructions` and can be overridden via
  `server.mcp.instructions` in the YAML config; MCP clients surface
  the string in the agent's system prompt, so it is the primary
  lever for teaching agents when to use `pf_*` tools.
- `tool.RegisterMCP` registers each enabled tool (Phase 1–4) with a JSON-schema
  input and a handler that re-uses the same `tool.HandleX` functions
- **Streamable-http** transport: `mcpserver.NewStreamableHTTPServer(...)`
  exposes the MCPServer as an `http.Handler` mounted on `/mcp`. Modern
  MCP clients (Claude Code, etc.) speak this.
- **Legacy SSE** transport (opt-out via `server.mcp.sse_enabled: false`):
  `mcpserver.NewSSEServer(...)` produces an `SSEServer` whose
  `SSEHandler()` is mounted at `GET /sse` and whose `MessageHandler()`
  is mounted at `POST /message`. Claude Desktop and other SSE-only
  clients connect here. `GET /sse` opens a persistent
  `text/event-stream`, emits an initial `endpoint` event with a
  `sessionId`, and streams JSON-RPC responses back as `message`
  events; the paired POST hits `/message?sessionId=…`, returns 202,
  and dispatches via the shared MCPServer — the response comes back
  on the open SSE stream.
- MCP tool results are wrapped in a single `TextContent` block containing the
  JSON-encoded output (idiomatic mcp-go pattern)

All three transports (streamable-http, legacy SSE, REST) share the same
dispatcher instance, so filters, audit logging, and error mapping fire
identically regardless of entry point. Both MCP transports additionally
share a single `MCPServer`, so tool registrations and the
`initialize`-time instructions string are identical across them.

## Configuration contract

The entire binary is a runtime for a single YAML file. There are **no**
hardcoded paths, URLs, or identifiers in the code. All specificity lives in
the config — see `docs/config-doc.md`.

## Future phases

See `plan.md` §10 for the full roadmap. Short version:

- **Phase 2 (shipped in 0.3.0):** subagent / subprocess / http backends, `pf_fault` (deep retrieval), `pf_ps` (list agents), plus matching CLI subcommands.
- **Phase 3 (shipped in 0.4.0):** `RedactionFilter`, JSON / markdown-with-metadata context formats, `/api/openapi.json`, opt-in CORS, per-caller rate limiting, `HealthChecker` interface + richer `/health`, structured REST error envelope.
- **Phase 4 (shipped in 0.5.0):** write support via `pf_poke` — filesystem `WritableBackend` with `write_paths`/`write_mode`/`max_entry_size`/`flock`, direct append via `internal/write`, entry-template formatting, and `mode:"agent"` writeback that delegates to a subagent.
- **Phase 5:** OAuth2, caching, streaming, metrics
