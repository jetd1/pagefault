# pagefault — Personal Memory Service

> When your agent hits a context miss, pagefault loads the right page back in.

## 0. Development Guide

This section is **required reading** for anyone (human or AI) contributing to pagefault.

### Documentation Requirements

**README.md** — Always keep up to date. Must contain:
- One-paragraph description and the pagefault metaphor
- Quick start (build, configure, run)
- The 3 most recent changelog entries (synced from CHANGELOG.md)
- Link to full docs in `docs/`

**CLAUDE.md** — The AI-assistant development guide (like this section, but as a standalone file). Must contain:
- Quick reference: build commands, test commands, directory tree
- File-level TLDR for every file in the repo (one line each) — this is the primary navigation aid for agents
- Architecture overview (condensed from plan.md)
- Common development tasks (add a backend, add a tool, add a filter)
- Conventions and rules

**Update CLAUDE.md whenever:**
- A new file is created or deleted
- A package's responsibility changes
- A new development pattern is established
- The directory tree changes

### Documentation in `docs/`

All non-trivial subsystems get their own doc in `docs/`. Required docs:

| File | Content |
|------|---------|
| `docs/api-doc.md` | Full MCP + REST tool reference: every tool's input schema, output schema, error cases, and example request/response. Auto-generated sections are acceptable if kept in sync. |
| `docs/config-doc.md` | Full YAML config reference: every field, type, default, and description. Group by section (server, auth, backends, contexts, tools, filters, audit). Include at least one complete example per backend type. |
| `docs/architecture.md` | Architecture deep dive: request flow, backend plugin model, filter pipeline, auth layer, transport details. Diagrams welcome. |
| `docs/security.md` | Security model: threat model, auth mechanisms, filter behavior, write safety, audit format. |

Update the relevant doc whenever the corresponding code changes. Stale docs are worse than no docs.

### Directory Tree in CLAUDE.md

Maintain a full directory tree with one-line TLDRs in CLAUDE.md. Format:

```
pagefault/
├── cmd/pagefault/main.go          # CLI entry point: serve, token subcommands
├── internal/
│   ├── server/server.go           # HTTP server: chi router, MCP + REST mounts
│   ├── config/config.go           # Config structs, YAML loader, env substitution
│   ... (every file)
```

This is the **first thing** an agent reads to orient itself. Keep it accurate.

### Versioning and Changelog

- Version is in a `VERSION` file at repo root (single line, e.g., `0.1.0`) and echoed by the binary (`pagefault --version`).
- **Bump the version before every commit that changes behavior:**
  - Bug fixes, minor tweaks, small refactors: **patch** bump (e.g., `0.1.0` → `0.1.1`)
  - New features, new tools, new config fields, new backends: **minor** bump (e.g., `0.1.1` → `0.2.0`)
  - **Never** bump the major version unless explicitly asked.
- **Update `CHANGELOG.md`** whenever the version changes. Add an entry under `## X.Y.Z (YYYY-MM-DD)` with `### Added`, `### Changed`, `### Removed`, `### Fixed` subsections as appropriate.
- **Always document breaking changes**: renamed config fields, removed tools, changed response shapes. Include migration guidance (old → new).
- **Before bumping the version**, run a full check:
  1. `make test` passes
  2. `make lint` passes
  3. All `.md` files are up to date (README, CLAUDE.md, docs/api-doc.md, docs/config-doc.md, CHANGELOG.md)
  4. Directory tree in CLAUDE.md matches reality
  5. Version string in `VERSION` matches the changelog entry
- **Keep the 3 most recent changelog entries in `README.md`** under a "Recent Changes" section.

### Testing

- **Every phase must have tests before it's considered complete.** No exceptions.
- Tests live alongside source files (`internal/backend/filesystem_test.go`).
- **Minimum test coverage per module:**
  - Config: parse valid YAML, reject invalid YAML, env substitution, default values
  - Backends: read, search, list, sandbox enforcement, glob matching, error cases
  - Auth: valid token, invalid token, expired token, missing header, none mode
  - Filters: allow/deny globs, tag matching, redaction regex, disabled filters pass-through
  - Tools: input validation, dispatch to correct backend, error formatting
  - Server: health endpoint, MCP mount responds, REST mount responds, auth middleware rejects
  - Write: append, format entry, flock behavior, write_paths enforcement, max_entry_size
- **Integration tests** use `httptest.NewServer` — spin up the full server with a test config, call tools via HTTP, verify responses.
- **Table-driven tests** preferred (idiomatic Go): `[]struct{ name, input, want }`.
- **Test data** goes in `testdata/` directories alongside test files. Do not use `/tmp` for test fixtures.
- Run `make test` before every commit.

### Code Conventions

- **Go style**: `gofmt`, `go vet`, `staticcheck` must pass. Run `make lint`.
- **Interfaces**: accept interfaces, return concrete structs.
- **Context**: `context.Context` as first param in all methods that do I/O or could block.
- **Errors**: use `fmt.Errorf("...: %w", err)` for wrapping. Use sentinel errors (`var ErrX = errors.New(...)`) for programmatic checks. Check with `errors.Is` / `errors.As`.
- **Logging**: use `log/slog` (structured logging). No `fmt.Println` in library code.
- **Naming**: Go conventions — `NewFilesystemBackend`, not `CreateFilesystemBackend`. Acronyms are all-caps (`URI`, `HTTP`), not `Uri`, `Http`.
- **Comments**: Godoc on all exported types and functions. Package-level doc comment in every package.
- **Commits**: conventional commits (`feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`). Append `Co-Authored-By:` trailer as appropriate.

### What NOT to Do

- Do NOT import anything from OpenClaw, Hermes, or any deployment-specific package
- Do NOT hardcode any paths, URLs, IPs, or user identifiers in code
- Do NOT assume a specific OS, shell, or filesystem layout
- Do NOT add caching in Phase 1 (YAGNI)
- Do NOT add streaming responses in Phase 1
- Do NOT build Docker/systemd/Caddy configs — that's post-deploy infra, not part of the binary
- Do NOT skip writing tests
- Do NOT change config schema without updating docs/config-doc.md
- Do NOT add a tool without updating docs/api-doc.md
- When in doubt, make it configurable rather than hardcoded

## 1. What Is This

pagefault is a **config-driven memory server** that exposes personal knowledge (files, search indices, agent sessions) to external AI clients via **MCP** and **OpenAPI** transports.

It solves one problem: you have rich, structured memory on one machine (daily notes, long-term memory, conversation summaries, agent context files), and you want any AI client on any device (Claude Code on MacBook, Claude app on iPhone, ChatGPT, Cursor, etc.) to query it on demand — without syncing files, without full agent sessions, and with fine-grained access control.

**The metaphor:** In an OS, a page fault occurs when a process accesses memory not currently loaded — the handler fetches it from backing store and resumes execution. pagefault does the same for AI agents: when they need context they don't have, they fault to this server, which loads the right information from configured backends.

## 2. Design Principles

| # | Principle | Implication |
|---|-----------|-------------|
| 1 | **Config-driven, not code-driven** | All behavior (backends, tools, filters, auth, contexts) is defined in a YAML config. The server is a runtime for that config. |
| 2 | **Framework is generic; deployment is specific** | Zero hardcoded paths, zero infra assumptions, zero client-specific logic in core. All specificity lives in config files. |
| 3 | **Pluggable backends** | Data sources are backend plugins implementing a common interface. Filesystem, subprocess, HTTP, subagent — all are backends. |
| 4 | **Filters are optional and composable** | Path allowlist/denylist, tag filtering, content redaction — each can be enabled/disabled independently. Can be turned off entirely. |
| 5 | **Auth is a thin layer** | Default: bearer tokens. Can be disabled for local dev. Production auth is expected to be handled by a reverse proxy (e.g., Hermes, Caddy with forward_auth). The server just reads a trusted header or validates a token. |
| 6 | **Subagent spawning is first-class** | `pf_fault` tool spawns a real subagent (via CLI or HTTP), waits for a real result, and returns it. Not a simulated search — a real agent turn. |
| 7 | **Dual transport: MCP + OpenAPI** | MCP for Claude family clients. OpenAPI REST for ChatGPT Actions, curl, and any HTTP client. Same tool logic, two doors. |
| 8 | **Audit everything** | Every tool call is logged with caller, tool, args, timing, result size. No silent access. |

## 3. Architecture

```
┌──────────────────────────────────────────────────┐
│  Clients (Claude Code, Claude iOS, ChatGPT, etc) │
└────────────┬──────────────────────┬──────────────┘
             │ MCP (streamable-http)│ REST (OpenAPI)
             ▼                      ▼
┌──────────────────────────────────────────────────┐
│  pagefault server (Go + mcp-go)                   │
│  ┌──────────┐  ┌───────────────────────────┐     │
│  │ Auth     │  │ Tool Dispatcher           │     │
│  │ (bearer  │  │  pf_maps                  │     │
│  │  /header │  │  pf_load                  │     │
│  │  /none)  │  │  pf_scan                  │     │
│  │          │  │  pf_peek                  │     │
│  │          │  │  pf_fault → subagent      │     │
│  │          │  │  pf_ps                    │     │
│  └──────────┘  └───────────┬───────────────┘     │
│                            │                      │
│  ┌─────────────┐  ┌───────┴────────┐             │
│  │ Filters     │  │ Backend Registry│             │
│  │ (allow/deny │  │  filesystem     │             │
│  │  /redact/   │  │  subprocess     │             │
│  │  tags)      │  │  http           │             │
│  │  —optional— │  │  subagent-cli   │             │
│  └─────────────┘  │  subagent-http  │             │
│                    └───────┬────────┘             │
│                            │                      │
│  ┌─────────────────────────┴──────────────┐       │
│  │ Audit Logger (JSONL)                    │       │
│  └─────────────────────────────────────────┘       │
└──────────────────────────────────────────────────┘
```

### Request Flow

1. Client calls a tool via MCP or REST
2. Auth layer identifies the caller (token → identity, or trusted header)
3. Tool dispatcher validates params, resolves which backend(s) to query
4. **Pre-filter**: path/tag allowlist + denylist check (if enabled)
5. Backend executes the query (read file, run subprocess, spawn subagent, etc.)
6. **Post-filter**: content redaction (if enabled)
7. Audit log entry is written
8. Result returned to client

## 4. Core Abstractions

### 4.1 Backend

```go
type Resource struct {
    URI         string            `json:"uri"`          // e.g. "memory://2026-04-10.md"
    Content     string            `json:"content"`      // the actual content
    ContentType string            `json:"content_type"` // "text/markdown", "application/json", etc.
    Metadata    map[string]any    `json:"metadata"`     // source, tags, size, mtime, etc.
}

type SearchResult struct {
    URI      string         `json:"uri"`
    Snippet  string         `json:"snippet"`
    Score    *float64       `json:"score"`    // nil for non-ranking backends
    Metadata map[string]any `json:"metadata"`
}

// Backend is the interface that all data source plugins implement.
type Backend interface {
    Name() string
    Read(ctx context.Context, uri string) (*Resource, error)
    Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
    ListResources(ctx context.Context) ([]map[string]any, error)
}
```

### 4.2 Context

A **context** is a named, pre-composed bundle of backend results. Instead of making the client know file paths, they ask for a semantic context by name.

```yaml
contexts:
  - name: user-profile
    description: "User's personal profile, preferences, and setup"
    sources:
      - backend: fs
        uri: "memory://USER.md"
      - backend: fs
        uri: "memory://IDENTITY.md"
    format: markdown  # markdown | json
    max_size: 8000    # characters; truncate with summary if exceeded
```

Context resolution: load each source → concatenate / merge → apply filters → truncate if needed → return.

### 4.3 Subagent Backend

A special backend type that spawns a full agent process, gives it a task, and returns its final response. This is what makes `pf_fault` powerful — it's not a search, it's a real agent reasoning about your memory.

```go
// SubagentBackend extends Backend with agent spawning capability.
type SubagentBackend interface {
    Backend
    Spawn(ctx context.Context, agentID string, task string, timeout time.Duration) (string, error)
    ListAgents() []AgentInfo
}

type AgentInfo struct {
    ID          string `json:"id"`
    Description string `json:"description"`
}
```

Built-in implementations:
- **`subagent-cli`**: Runs a shell command, waits for stdout. Configurable command template with `{agent_id}` and `{task}` placeholders.
- **`subagent-http`**: POSTs to an HTTP endpoint, waits for JSON response. Configurable URL template and headers.

### 4.4 Filter Pipeline

```go
type Filter interface {
    AllowURI(uri string, caller *Caller) bool
    FilterContent(content string, uri string) string
}

// CompositeFilter chains multiple filters.
// URI: AND (all must pass). Content: sequential application.
type CompositeFilter struct {
    filters []Filter
}
```

Built-in filters:
- **PathFilter**: allowlist/denylist of URI glob patterns
- **TagFilter**: only allow resources with matching tags
- **RedactionFilter**: regex-based content replacement (e.g., mask API keys, phone numbers)

### 4.5 Auth

```go
type Caller struct {
    ID       string         `json:"id"`     // token ID or header value
    Label    string         `json:"label"`  // human-readable label
    Metadata map[string]any `json:"metadata"` // extra info from token record
}

type AuthProvider interface {
    Authenticate(r *http.Request) (*Caller, error)
}
```

Built-in:
- **BearerTokenAuth**: validates `Authorization: Bearer <token>` against a configured token file
- **TrustedHeaderAuth**: reads caller identity from a trusted header (e.g., `X-Forwarded-User` from a reverse proxy)
- **NoneAuth**: no auth, returns anonymous caller (for local dev)

## 5. Tool Surface

All tools are individually enable/disable-able in config. Default: all enabled.

Tool names follow a `pf_` prefix scheme borrowed from Unix memory management
and kernel debugging — `/proc/pid/maps`, page swap-in, `kswapd`-style scan,
debugger `PEEKDATA`/`POKEDATA`, real page faults, `ps`. The mapping:

| Wire name   | Role                                      | Phase |
|-------------|-------------------------------------------|-------|
| `pf_maps`   | list pre-composed memory regions          | 1     |
| `pf_load`   | load a region into working memory         | 1     |
| `pf_scan`   | scan backends for matching content        | 1     |
| `pf_peek`   | read a specific resource (optional slice) | 1     |
| `pf_fault`  | the real page fault — trigger a subagent  | 2     |
| `pf_ps`     | list configured subagents                 | 2     |
| `pf_poke`   | writeback, paired with `pf_peek`          | 4     |

Internal Go names (`HandleListContexts`, `GetContextInput`, etc.) retain
their generic form for code clarity — see `CLAUDE.md` for the wire ↔ code
mapping.

### 5.1 `pf_maps`

Returns all available memory regions (contexts) with names and descriptions. Zero-cost, no backend calls.

**Input:** none
**Output:**
```json
[
  {"name": "user-profile", "description": "User's personal profile, preferences, and setup"},
  {"name": "recent-activity", "description": "Daily notes from the last N days"}
]
```

### 5.2 `pf_load`

Load and return a pre-composed memory region (context) by name.

**Input:**
- `name` (string, required) — context name
- `format` (string, optional) — override output format: "markdown" | "json"

**Output:** The composed context content (truncated if exceeds max_size).

### 5.3 `pf_scan`

Scan configured backends for content matching a query — full-text and/or semantic depending on the backend. Fan-out to all backends, merge results.

**Input:**
- `query` (string, required) — search query (keywords, phrases, or natural language depending on backend)
- `limit` (int, optional, default 10) — max results
- `backends` (string[], optional) — restrict to specific backend names
- `date_range` (object, optional) — `{from: "YYYY-MM-DD", to: "YYYY-MM-DD"}` — hint for backends that support it

**Output:**
```json
[
  {"uri": "memory://2026-04-10.md", "snippet": "...matched text...", "score": 0.92, "backend": "fs"},
  {"uri": "lcm://sum_abc123", "snippet": "...matched text...", "score": 0.85, "backend": "lcm"}
]
```

### 5.4 `pf_peek`

Peek at a specific resource by URI, optionally slicing a line range.

**Input:**
- `uri` (string, required) — resource URI (e.g. `memory://2026-04-10.md`)
- `from_line` (int, optional) — start line (1-indexed) for text resources
- `to_line` (int, optional) — end line for text resources

**Output:** Full resource content (or slice).

### 5.5 `pf_fault`

The real page fault. Spawn a full subagent to do comprehensive retrieval from backing store — the agent has access to all tools (LCM, memory search, file read, session history) and can reason about what's relevant. This is the deepest-cost operation in the tool surface, matching the metaphor: a `pf_peek` misses, so we take a real fault and page the content in.

**Input:**
- `query` (string, required) — what to find / understand
- `agent` (string, optional) — which agent to spawn (default: first configured subagent)
- `timeout_seconds` (int, optional, default 120) — max wait time

**Output:**
```json
{
  "answer": "The agent's synthesized response...",
  "agent": "wocha",
  "elapsed_seconds": 47.3,
  "sources": ["memory://2026-04-10.md", "lcm://sum_abc123"]
}
```

If the subagent times out, return:
```json
{
  "error": "Subagent timed out after 120s",
  "agent": "wocha",
  "partial_result": null
}
```

### 5.6 `pf_ps`

List available subagents (names + descriptions), `ps`-style. Allows clients to know which agents they can request for `pf_fault`.

**Input:** none
**Output:**
```json
[
  {"id": "wocha", "description": "Full-featured dev agent with Feishu, LCM, and workspace access"},
  {"id": "main", "description": "Primary personal agent with full tool access"}
]
```

### 5.7 `pf_poke`

Poke content back into memory — the write counterpart to `pf_peek`. Supports two modes: **direct append** (fast, zero-token, for simple entries) and **agent writeback** (spawns a subagent to intelligently decide where and how to write).

**Design rationale:** External agents often generate insights worth persisting — e.g., "Fixed auth bug" to daily notes, or "Jet prefers X" to long-term memory. Direct append covers the 80% case (fixed format, known location). Agent writeback covers the 20% case (needs judgment about where to write, how to format, whether to merge with existing content).

**Input:**
- `uri` (string, required for `mode: "direct"`) — target resource URI (e.g. `memory://memory/2026-04-10.md`)
- `content` (string, required) — the content to write
- `mode` (string, required) — `"direct"` | `"agent"`
- `format` (string, optional, default `"entry"`) — only for `mode: "direct"`:
  - `"entry"` — auto-wrap as a timestamped entry: `\n---\n## [HH:MM] via pagefault\n\n{content}\n`
  - `"raw"` — append content as-is (requires `write_mode: "any"` in config)
- `agent` (string, optional) — which subagent to use for `mode: "agent"` (default: first configured)
- `target` (string, optional, default `"auto"`) — only for `mode: "agent"`: hint for the subagent
  - `"auto"` — subagent reads existing files and decides the best location
  - `"daily"` — write to today's daily note
  - `"long-term"` — write to MEMORY.md or equivalent
  - `"self-improving"` — write to self-improving domain
  - Any custom string — passed as-is to the subagent as a hint

**Output (mode: "direct"):**
```json
{
  "status": "written",
  "uri": "memory://memory/2026-04-10.md",
  "bytes_written": 142,
  "mode": "direct",
  "format": "entry"
}
```

**Output (mode: "agent"):**
```json
{
  "status": "written",
  "agent": "wocha",
  "elapsed_seconds": 23.5,
  "result": "Appended to memory/2026-04-10.md as a new entry under 'OpenClaw Debugging' section.",
  "targets_written": ["memory://memory/2026-04-10.md"]
}
```

**Error cases:**
- Backend is not writable → `403 AccessViolation: backend is read-only`
- URI not in `write_paths` allowlist → `403 AccessViolation: write path not allowed`
- Content exceeds `max_entry_size` → `413 ContentTooLarge: entry exceeds max_entry_size`
- `format: "raw"` but `write_mode` is `"append"` → `400 InvalidRequest: raw format requires write_mode: any`
- Subagent times out → `504 SubagentTimeout: agent writeback timed out`

## 6. Configuration Schema

The entire server is driven by a single YAML file. This is the *contract* — the server is just a runtime for it.

```yaml
# pagefault.yaml — Full schema reference

# ── Server ──────────────────────────────────────
server:
  host: "0.0.0.0"
  port: 8444
  # Base URL for OpenAPI spec generation (used by ChatGPT Actions, etc.)
  public_url: "https://pagefault.jetd.one"

# ── Auth ────────────────────────────────────────
auth:
  # "none" | "bearer" | "trusted_header"
  mode: "bearer"

  bearer:
    # Path to JSONL tokens file, one JSON object per line:
    # {"id": "macbook-cc", "token": "pf_xxx...", "label": "Claude Code on MacBook"}
    tokens_file: "/etc/pagefault/tokens.jsonl"

  trusted_header:
    # Header name that carries the authenticated user identity
    # Set by a reverse proxy (Hermes, Caddy forward_auth, etc.)
    header: "X-Forwarded-User"
    # Optional: require that the request comes from a trusted proxy IP
    trusted_proxies: ["127.0.0.1", "192.168.50.224"]

# ── Backends ────────────────────────────────────
# Each backend has a unique name and a type.
# "type" determines which implementation class to use.
# Additional keys are type-specific config.

backends:
  - name: fs
    type: filesystem
    root: "/home/jet/.openclaw/workspace"
    # Only files matching these globs are visible (relative to root)
    include: ["memory/**/*.md", "AGENTS.md", "USER.md", "SOUL.md", "IDENTITY.md", "TOOLS.md"]
    # Even within include, these are excluded
    exclude: ["memory/intimate.md", "memory/cha-fun-facts.md"]
    # URI scheme for this backend
    uri_scheme: "memory"
    # Automatically tag resources by path pattern
    auto_tag:
      "memory/**/*.md": ["daily", "memory"]
      "AGENTS.md": ["config", "bootstrap"]
      "USER.md": ["config", "bootstrap", "profile"]
    # Sandbox: never serve files outside root, even with symlinks
    sandbox: true
    # ── Write config (all optional, defaults to read-only) ──
    writable: true
    # Only these URI patterns are allowed for writes (glob matching)
    write_paths:
      - "memory://memory/20*.md"        # daily notes
      - "memory://memory/todos.md"      # todos
      - "memory://MEMORY.md"            # long-term memory
    # Write mode: "append" (only append) | "any" (append, prepend, overwrite)
    write_mode: "append"
    # Maximum single entry size in characters
    max_entry_size: 2000
    # File locking: "flock" (POSIX) | "none" (no locking, not recommended for writable backends)
    file_locking: "flock"

  - name: self-improving
    type: filesystem
    root: "/home/jet/.openclaw/self-improving"
    include: ["**/*.md"]
    exclude: []
    uri_scheme: "self-improving"
    auto_tag:
      "**/*.md": ["self-improving", "meta"]
    sandbox: true

  - name: rg
    type: subprocess
    # Command template. {query} is replaced with the search query (shell-escaped).
    command: "rg --json -i -n --max-count 20 '{query}' {roots}"
    # Roots to search (substituted into {roots})
    roots:
      - "/home/jet/.openclaw/workspace/memory"
      - "/home/jet/.openclaw/self-improving"
    timeout: 10
    # Parse stdout as JSON lines (ripgrep --json format)
    parse: "ripgrep_json"

  - name: lcm
    type: http
    # Base URL for the LCM/search API
    base_url: "http://127.0.0.1:6443"
    # Auth for this backend (can differ from server auth)
    auth:
      mode: "bearer"
      token: "${OPENCLAW_GATEWAY_TOKEN}"  # env substitution
    search:
      method: "POST"
      path: "/api/lcm/search"
      body_template: '{"query": "{query}", "limit": {limit}}'
      response_path: "$.results"  # JSONPath to extract results
    timeout: 15

  - name: openclaw
    type: subagent-cli
    # Command template to spawn an OpenClaw agent
    # {agent_id} and {task} are substituted at runtime
    command: "openclaw agent run --agent {agent_id} --task '{task}' --timeout {timeout} --format plain"
    timeout: 300  # default timeout in seconds
    # Available agents (for pf_ps tool)
    agents:
      - id: wocha
        description: "Dev agent with Feishu, LCM, workspace, and coding tools"
      - id: main
        description: "Primary personal agent with full tool access"

  # Alternative: subagent via HTTP (for gateway API access)
  # - name: openclaw-http
  #   type: subagent-http
  #   base_url: "https://localhost:6443/api"
  #   auth:
  #     mode: "bearer"
  #     token: "${OPENCLAW_GATEWAY_TOKEN}"
  #   spawn:
  #     method: "POST"
  #     path: "/agents/{agent_id}/run"
  #     body_template: '{"task": "{task}", "timeout": {timeout}}'
  #     response_path: "$.result"
  #   timeout: 300
  #   agents:
  #     - id: wocha
  #       description: "Dev agent with Feishu, LCM, workspace, and coding tools"

# ── Contexts ────────────────────────────────────
# Pre-composed bundles that clients can request by name.

contexts:
  - name: user-profile
    description: "User's personal profile, preferences, and setup"
    sources:
      - backend: fs
        uri: "memory://USER.md"
      - backend: fs
        uri: "memory://IDENTITY.md"
    format: markdown
    max_size: 8000

  - name: agent-bootstrap
    description: "Agent initialization docs (AGENTS.md, SOUL.md, TOOLS.md) — filtered for external agents"
    sources:
      - backend: fs
        uri: "memory://AGENTS.md"
      - backend: fs
        uri: "memory://SOUL.md"
      - backend: fs
        uri: "memory://TOOLS.md"
    format: markdown
    max_size: 16000

  - name: recent-activity
    description: "Daily notes from the last N days"
    sources:
      # Dynamic source: resolved at query time
      - backend: fs
        uri: "memory://recent"  # special URI pattern resolved by filesystem backend
        params:
          days: 7
          glob: "*.md"
    format: markdown
    max_size: 24000

  - name: self-improving
    description: "Agent self-improvement lessons and corrections"
    sources:
      - backend: self-improving
        uri: "self-improving://memory.md"
    format: markdown
    max_size: 8000

# ── Tools ───────────────────────────────────────
# Enable/disable individual tools. Default: all enabled.

tools:
  pf_maps:  true
  pf_load:  true
  pf_scan:  true
  pf_peek:  true
  pf_fault: true
  pf_ps:    true
  pf_poke:  true  # writeback support (direct append + agent writeback)

# ── Filters ─────────────────────────────────────
# Optional. Can be disabled entirely with `enabled: false`.

filters:
  enabled: true

  # Path-level: evaluated BEFORE backend access
  path:
    # If allow is set, ONLY these URIs are accessible
    allow: []
    # These URIs are always blocked, even if in allow list
    deny:
      - "memory://memory/intimate.md"
      - "memory://memory/cha-fun-facts.md"
      - "self-improving://**/corrections.md"  # glob supported

  # Tag-level: only serve resources with these tags
  tags:
    # If set, ONLY resources with at least one matching tag are served
    allow: []
    deny: []

  # Content-level: applied AFTER backend returns content
  redaction:
    enabled: false
    rules:
      - pattern: '(?i)(api[_-]?key|token|secret|password)\s*[:=]\s*\S+'
        replacement: '[REDACTED]'
      - pattern: '\b\d{16,19}\b'  # credit card numbers
        replacement: '[REDACTED]'

# ── Audit ───────────────────────────────────────
audit:
  enabled: true
  # "jsonl" (append-only file) | "stdout" | "off"
  log_path: "/var/log/pagefault/audit.jsonl"
  # Include full result content in audit (warning: large)
  include_content: false
```

## 7. Transport Details

### MCP (Primary)

- Endpoint: `POST /mcp` (streamable-http transport)
- Tools are registered as MCP tools using mcp-go tool definitions with JSON Schema
- mcp-go handles protocol details (session management, JSON-RPC, SSE streaming)
- Auth: Bearer token in `Authorization` header (standard MCP auth pattern)

### OpenAPI (Secondary)

- Endpoints: `POST /api/{tool_name}` for each tool
- OpenAPI 3.0 spec at `GET /api/openapi.json`
- Used by ChatGPT Custom GPT Actions, curl, and any HTTP client
- Auth: Same as MCP (Bearer token in header)

Both transports dispatch to the **same** `ToolDispatcher` — zero logic duplication.

### Health / Meta

- `GET /health` → `{"status": "ok", "backends": {"fs": "ok", "lcm": "ok", ...}}`
- `GET /` → Basic info page with links to `/api/openapi.json` and `/health`

## 8. Project Structure

The canonical directory tree with file-level TLDRs lives in **`CLAUDE.md` §Directory Tree** — update that file when adding or moving files. This section used to duplicate the tree and inevitably drifted; pointing at one source of truth is simpler.

## 9. Tech Stack

| Component | Choice | Why |
|-----------|--------|-----|
| Language | Go 1.23+ | Single-binary deployment, goroutine concurrency, strong stdlib, zero runtime deps |
| MCP SDK | [mcp-go](https://github.com/mark3labs/mcp-go) | Mature Go MCP server library, streamable-http support, active community |
| HTTP | net/http (stdlib) + [chi](https://github.com/go-chi/chi) | Lightweight router, stdlib-compatible, no magic |
| Config | struct tags + `go-playground/validator` | Type-safe config with validation tags |
| YAML | `goccy/go-yaml` or `yaml.v3` | Config loading with env var substitution |
| HTTP client | net/http | Stdlib sufficient for backend calls; no external dep needed |
| JSON | `encoding/json` (stdlib) | Standard JSON handling |
| Glob | `gobwas/glob` or `path.Match` | URI pattern matching for filters |
| Testing | `testing` (stdlib) + `testify` | Standard Go testing with assertions |
| Build | `go build` + Makefile | `make build` → single binary in `./bin/` |

Why Go over Python:
- **Single binary**: `scp pagefault server:/usr/local/bin/` — no venv, no pip, no Python version issues
- **Goroutine concurrency**: multiple `pf_fault` calls in parallel, naturally
- **Subagent lifecycle**: `context.WithTimeout` + `exec.CommandContext` — clean process management
- **Runtime independence**: OpenClaw is Node/TS; pagefault in Go = one runtime crash can't take out both
- **Operations**: `systemd` runs a single binary, no wrapper scripts needed

## 10. Implementation Phases

### Phase 1 — MVP: Files + Basic Tools + Bearer Auth ✅ (shipped in 0.1.0–0.2.0)

A running server that serves files and searches from a directory with bearer auth, path/tag filters, JSONL audit, four tools (`pf_maps`, `pf_load`, `pf_scan`, `pf_peek`) over both MCP and REST, and matching `pagefault <tool>` CLI subcommands. `filesystem` backend only; no subagents, no subprocess / http backends, no redaction, no OpenAPI spec.

See `CHANGELOG.md` §0.1.0 and §0.2.0 for the exact per-release breakdown, and `CLAUDE.md` §Common Development Tasks for the "add a backend / tool / filter" recipes distilled from this work.

### Phase 2 — Subagents + More Backends ✅ (shipped in 0.3.0–0.3.2)

Four additional backend types — `subprocess` (rg/grep/plain), generic `http`, `subagent-cli` (tokenized argv, no shell), `subagent-http` (POST + bearer + JSON body template) — plus the `SubagentBackend` interface that wraps them for deep retrieval. Two new tools (`pf_fault`, `pf_ps`) exposed via MCP, REST, and CLI with the same dispatcher / filter / audit pipeline as Phase 1. Timeouts flow through `context.WithTimeout` at backend entry; `exec.CommandContext` kills CLI agents on expiry and the HTTP client cancels request contexts. `pf_fault` surfaces timeouts as `timed_out: true` + `partial_result` rather than as errors. `internal/server.errorStatus` maps `ErrAgentNotFound` → 404 and `ErrSubagentTimeout` → 504. `configs/openclaw.yaml` is deferred to a later phase as deployment-specific.

See `CHANGELOG.md` §0.3.0 / §0.3.1 / §0.3.2 for the detailed per-release notes.

### Phase 3 — Polish + Production ✅ (shipped in 0.4.0)

`RedactionFilter` (Go regexp with capture-group replacements, compiled at server start), JSON and `markdown-with-metadata` context formats for `pf_load` (JSON mode drops sources from the tail rather than byte-truncating so the emitted document stays valid), public `/api/openapi.json` generated live from the current config + dispatcher (ChatGPT Custom GPT Actions importable), opt-in `server.cors` with preflight support, per-caller in-process rate limiting via `server.rate_limit` keyed on `caller.id` (429 + `Retry-After` on over-budget), optional `HealthChecker` backend interface with the filesystem backend probing its root and `/health` reporting `ok` / `degraded` / `unavailable` per-backend, and a structured REST error envelope (`{"error":{"code","status","message"}}`) with stable snake_case codes for every sentinel. README gained client setup guides for Claude Code, Claude Desktop, and ChatGPT Custom GPT. Circuit-breaker-style backoff for flaky backends is deferred — `pf_scan`'s "one backend failing doesn't break search" already covers the common case.

See `CHANGELOG.md` §0.4.0 for the detailed breakdown.

### Phase 4 — Writeback (Read-Write)

Adding `pf_poke` tool with two modes: direct append and agent writeback.

**4a. Direct append (filesystem backend write support):**

1. `internal/write/writer.go` — `Writer` interface + `FilesystemWriter` implementation
   - `Append(ctx, uri, content) error` — atomic append with file locking (`flock`)
   - `WriteMode` enum: `AppendOnly`, `Any` (append, prepend, overwrite)
   - Validates URI against `write_paths` allowlist before writing
   - Enforces `max_entry_size` limit
   - Uses `os.OpenFile` with `O_APPEND|O_WRONLY` for atomic appends
2. `internal/write/format.go` — Entry formatting
   - `FormatEntry(content, format, caller) string` — wraps content as timestamped entry
   - `"entry"` format: `\n---\n## [HH:MM] via pagefault\n\n{content}\n`
   - `"raw"` format: content as-is (requires `write_mode: "any"`)
3. `internal/tool/write.go` — `pf_poke` tool handler for `mode: "direct"`
4. `internal/backend/filesystem.go` — extend with write support
   - `Writable() bool`, `WritePaths() []string`, `WriteMode() WriteMode`, `MaxEntrySize() int`
   - `Write(ctx, uri, content) error` — delegates to `FilesystemWriter`
5. `internal/config/config.go` — add `Writable`, `WritePaths`, `WriteMode`, `MaxEntrySize`, `FileLocking` fields to `FilesystemBackendConfig`
6. `internal/filter/filter.go` — extend `PathFilter` with write-specific allowlist (`write_paths`)
   - Read allowlist and write allowlist are separate (you can read broadly but write narrowly)
7. `internal/audit/audit.go` — log write operations with content hash (not full content)
8. Tests: `internal/write/writer_test.go`, `internal/write/format_test.go`, `internal/tool/write_test.go`
9. Update `configs/openclaw.yaml` with writable filesystem backend config
10. Update `docs/api-doc.md` with `pf_poke` tool
11. Update `docs/config-doc.md` with write-related config fields
12. Update `docs/security.md` with write threat model
13. Update `CLAUDE.md` directory tree
14. Version bump + CHANGELOG

**4b. Agent writeback (subagent-assisted):**

1. Extend `internal/tool/write.go` — handle `mode: "agent"`
   - Compose subagent task: `"A remote agent wants to record the following to memory: '{content}'. Target: {target}. Read the relevant memory files, decide the best location, and write it appropriately. Follow existing file conventions."`
   - Spawn subagent via `SubagentBackend.Spawn()`
   - Return subagent's response to the caller
2. The subagent itself uses its own write capabilities (it has full workspace access, not constrained by pagefault's write_paths). pagefault's `write_paths` only gates the `mode: "direct"` path — agent mode delegates trust to the subagent.
3. Tests: `internal/tool/write_agent_test.go` with mock subagent backend

**Security considerations for write:**
- **Default is read-only.** `writable: false` unless explicitly enabled.
- **Write allowlist is separate from read allowlist.** Even if a backend is writable, only `write_paths` URIs accept writes.
- **Append-only by default.** `write_mode: "append"` prevents overwrites. `write_mode: "any"` must be explicitly configured.
- **Size limit.** `max_entry_size` prevents dumping large content.
- **File locking.** `flock` prevents race conditions when Cha and Claude Code write simultaneously.
- **Agent mode trusts the subagent.** The subagent has its own write constraints (workspace rules, AGENTS.md). pagefault doesn't re-validate agent writes.
- **Audit.** Every write is logged (who, what URI, how many bytes, mode).

### Phase 5 — Hardening

1. OAuth2 auth provider
2. Caching layer (LRU in-process, or Redis)
3. Streaming for long subagent responses
4. Metrics endpoint (Prometheus)
5. Docker image
6. systemd unit file example
7. Update all docs
8. Version bump + CHANGELOG

## 11. OpenAPI Endpoint Mapping (for ChatGPT Actions)

Each MCP tool maps to a REST endpoint:

| MCP Tool   | REST Endpoint    | Method |
|------------|------------------|--------|
| `pf_maps`  | `/api/pf_maps`   | POST |
| `pf_load`  | `/api/pf_load`   | POST |
| `pf_scan`  | `/api/pf_scan`   | POST |
| `pf_peek`  | `/api/pf_peek`   | POST |
| `pf_fault` | `/api/pf_fault`  | POST |
| `pf_ps`    | `/api/pf_ps`     | POST |
| `pf_poke`  | `/api/pf_poke`   | POST |

All accept JSON bodies matching the MCP tool input schemas. All return JSON.

OpenAPI spec available at `/api/openapi.json` — paste this URL into ChatGPT Custom GPT Actions.

## 12. Security Model

### Threat: Unauthorized access
- **Mitigation:** Bearer tokens (per-device, revocable) or trusted-header auth behind a reverse proxy
- Tokens are never logged or included in audit records (only token ID + label)

### Threat: Path traversal
- **Mitigation:** Filesystem backend enforces `sandbox: true` — resolves symlinks, rejects paths outside `root`
- URI scheme mapping prevents arbitrary filesystem access

### Threat: Unauthorized writes
- **Mitigation:** Backends default to `writable: false`; must be explicitly enabled
- Separate `write_paths` allowlist — even writable backends only accept writes to explicitly listed URIs
- `write_mode: "append"` by default — prevents overwriting existing content
- `max_entry_size` limits individual write payloads
- `format: "entry"` auto-wraps content with timestamp, preventing raw injection into file headers
- `format: "raw"` requires `write_mode: "any"` — an additional opt-in gate
- File locking (`flock`) prevents race conditions from concurrent writers
- Agent writeback (`mode: "agent"`) delegates to a subagent that has its own safety constraints
- All writes are audit-logged

### Threat: Sensitive data exposure
- **Mitigation:** `filters.path.deny` blocks specific URIs (e.g., intimate.md, financial details)
- `filters.redaction` masks patterns in content (API keys, credit cards)
- Tags allow coarse-grained access control
- All filters are **optional** — can be disabled for trusted environments

### Threat: Data leaving the perimeter
- **Acknowledgment:** Any content returned to an MCP client enters the model provider's API (Anthropic, OpenAI, etc.)
- This is the same trust boundary as using Claude or ChatGPT directly
- Filters exist to keep the most sensitive content off this path entirely

### Threat: Token theft (phone lost, etc.)
- **Mitigation:** Per-device tokens with `pagefault token revoke <id>`
- Audit log shows exactly what each token accessed

## 13. Open Questions

Questions from earlier phases have been resolved by shipped work
(OpenClaw CLI shape → configurable via `subagent-cli` template;
mcp-go chi mounting → verified in `internal/server`; multi-backend
merging → interleave, no cross-ranking; context response format →
Phase 3 ships `markdown`, `markdown-with-metadata`, and `json`).
What's still open:

1. **Write concurrency.** When two writers (e.g. Cha via OpenClaw and an
   external agent via pagefault) append to the same daily note, `flock`
   serializes the writes but the entry order depends on lock-acquisition
   order. Is that acceptable, or do we need a write queue with ordering
   guarantees? Phase 4.
2. **Agent writeback trust boundary.** `mode: "agent"` will bypass
   pagefault's `write_paths` allowlist because the subagent writes
   directly (not through pagefault). Should pagefault validate the
   subagent's write result against `write_paths`, or fully trust the
   subagent's judgment? Current lean: full trust — the subagent already
   has workspace-level access. Phase 4.

## 14. For Claude Code: How to Start

Phases 1, 2, and 3 have shipped — the foundations (filesystem backend, MCP + REST transports, four additional backend types, the six `pf_*` tools, CLI subcommands, auth, path/tag/redaction filters, audit, live OpenAPI spec, opt-in CORS and rate limiting, structured error envelope) all exist in working form. The original step-by-step Phase-1 build order lived here but was removed once it became historical; for *new* work the canonical entry points are:

- **`CLAUDE.md`** — directory tree, build/test commands, per-task recipes ("add a backend / tool / filter"), conventions, and rules. Read this first.
- **Section 0** of this file — conventions and non-negotiables from the project's early days that still apply.
- **`CHANGELOG.md`** — per-release history.

### Before writing any code

1. Read **`CLAUDE.md`** top-to-bottom for the current layout and build commands.
2. Skim **Section 0** of this file for conventions.
3. Check **`CHANGELOG.md`** for the most recent release notes so you know what just landed.
4. If anything is ambiguous, ask before guessing. Do NOT introduce any dependency on OpenClaw, Hermes, or any specific infrastructure in the core code — the framework is generic; all specificity goes in config files.

### Style & conventions

See **Section 0 (Development Guide)** above and **`CLAUDE.md` §Conventions**.

### What NOT to do

See **Section 0 (Development Guide)** above and **`CLAUDE.md` §Rules**.

## 15. Relationship to OpenClaw (Informative Only)

This section is for **context only** — it does NOT affect the code. The core framework is agnostic.

pagefault is designed to work alongside an OpenClaw instance. In Jet's setup:

- **OpenClaw** runs on a Pop-OS workstation (IP 192.168.50.31) with:
  - Gateway on port 6443 (TLS, trusted-proxy auth behind Hermes SSO)
  - Two agents: `main` (Cha, personal) and `wocha` (dev/engineering)
  - LCM (Lossless Context Management) for conversation history compaction/recall
  - QMD (local memory search) for file-based semantic search
  - Workspace at `~/.openclaw/workspace/` with `memory/`, `MEMORY.md`, etc.
  - Self-improving memory at `~/.openclaw/self-improving/`

- **Hermes** provides SSO + reverse proxy for home-lab services on `*.jetd.one`
  - Issues one-time OTPs and persistent API tokens
  - Caddy does TLS termination and forwards `X-Hermes-User` after auth
  - pagefault will sit behind Caddy/Hermes at `pagefault.jetd.one` (or similar)

- **Subagent spawning** via OpenClaw:
  - CLI: `openclaw agent run --agent wocha --task "..."` (exact command TBD)
  - HTTP: Gateway API at `https://localhost:6443/api/...` (exact endpoints TBD)
  - The subagent has full access to LCM, memory search, workspace files, Feishu tools, etc.
  - This is what makes `pf_fault` powerful — it's a real agent, not a search index

All of this is expressed through the YAML config. The server code never imports or references OpenClaw.
