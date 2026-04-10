# pagefault — Architecture

This document describes the runtime architecture of pagefault. It is a
condensed version of `plan.md` §3–5 and should match the code in
`internal/`. If it drifts, update this doc.

## The one-line description

pagefault is a **config-driven memory server** that exposes personal
knowledge (files, search indices, agent sessions) to external AI clients via
MCP and REST.

## Component overview

```
┌──────────────────────────────────────────────────┐
│  Clients (Claude Code, Claude iOS, ChatGPT, etc) │
└────────────┬──────────────────────┬──────────────┘
             │ MCP (streamable-http)│ REST (POST /api/*)
             ▼                      ▼
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
| `internal/backend`      | `Backend` / `SubagentBackend` interfaces and five built-in types: `filesystem` (P1), `subprocess`, `http`, `subagent-cli`, `subagent-http` (P2) |
| `internal/auth`         | `AuthProvider` interface, Bearer/None/TrustedHeader, middleware |
| `internal/filter`       | `Filter` interface, `CompositeFilter`, PathFilter, TagFilter, RedactionFilter (P3) |
| `internal/audit`        | `Logger` interface, JSONL/stdout/nop sinks, arg sanitization |
| `internal/dispatcher`   | Central tool router: filter + backend + audit pipeline, markdown / markdown-with-metadata / json context formats |
| `internal/tool`         | Per-tool Handle\* functions and MCP registrations |
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
         audit.Log
         │
         ▼
         response
```

- **AllowURI** is called before the backend is touched. A denied URI never
  hits disk.
- **AllowTags** runs after the backend returns, with the resource's tag set
  (from `auto_tag` config rules on the filesystem backend).
- **FilterContent** runs `RedactionFilter` (Phase 3) when rules are
  configured; otherwise it is the identity function. The un-redacted
  copy never leaves the dispatcher.

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

**FilesystemBackend** (Phase 1). Responsibilities:

- Map URIs (`memory://foo.md`) to filesystem paths under the configured root
- Enforce an include/exclude glob filter (doublestar syntax)
- Enforce a sandbox that rejects symlinks escaping the root
- Auto-tag resources by path pattern
- Serve Read / Search / ListResources

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
  `Spawn(ctx, agentID, task, timeout)` and `ListAgents()`. `pf_fault`
  calls `Spawn`; `pf_ps` calls `ListAgents` across every configured
  `SubagentBackend`.

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
`ListContexts`, `GetContext`, `Search`, `Read`, `DeepRetrieve`, and
`ListAgents` methods.

## Transport details

### REST

- chi router with `/api` sub-router
- Auth middleware applied to the authenticated group
- `restHandler[In, Out]` generic adapter converts pure `tool.HandleX`
  functions into `http.HandlerFunc`s (JSON decode → caller extract → handler
  → error status mapping → JSON encode)

### MCP

- `mcpserver.NewMCPServer("pagefault", Version, WithToolCapabilities(true))`
- `tool.RegisterMCP` registers each enabled tool (Phase 1-2) with a JSON-schema
  input and a handler that re-uses the same `tool.HandleX` functions
- `mcpserver.NewStreamableHTTPServer(...)` exposes the server as an
  `http.Handler` mounted on `/mcp`
- MCP tool results are wrapped in a single `TextContent` block containing the
  JSON-encoded output (idiomatic mcp-go pattern)

Both transports share the same dispatcher instance, so filters and audit
fire identically regardless of entry point.

## Configuration contract

The entire binary is a runtime for a single YAML file. There are **no**
hardcoded paths, URLs, or identifiers in the code. All specificity lives in
the config — see `docs/config-doc.md`.

## Future phases

See `plan.md` §10 for the full roadmap. Short version:

- **Phase 2 (shipped in 0.3.0):** subagent / subprocess / http backends, `pf_fault` (deep retrieval), `pf_ps` (list agents), plus matching CLI subcommands.
- **Phase 3 (shipped in 0.4.0):** `RedactionFilter`, JSON / markdown-with-metadata context formats, `/api/openapi.json`, opt-in CORS, per-caller rate limiting, `HealthChecker` interface + richer `/health`, structured REST error envelope.
- **Phase 4:** write support (direct append + agent writeback)
- **Phase 5:** OAuth2, caching, streaming, metrics
