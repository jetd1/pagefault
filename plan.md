# pagefault — Personal Memory Service

> When your agent hits a context miss, pagefault loads the right page back in.

## 0. Development Guide

Development conventions, directory tree, build/test commands, "add a
backend / tool / filter" recipes, versioning rules, and the rules list
all live in **`CLAUDE.md`** — it is the primary navigation aid for
contributors (human or AI) and the single source of truth for how the
codebase is laid out and how work lands. Start there.

`plan.md` (this file) is the **product spec and phase roadmap** —
what exists, what's next, what was deferred, and the design
rationale behind each piece. It does not duplicate CLAUDE.md's
per-task recipes.

Two non-negotiables worth restating here because they shape the
spec below: (1) all behaviour is config-driven — no hardcoded paths,
URLs, or client-specific logic in core code; and (2) the framework
stays generic — nothing in `internal/` may import from OpenClaw,
Hermes, or any deployment-specific package.

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
| 6 | **Subagent spawning is first-class** | `pf_fault` (retrieve) and `pf_poke` mode:agent (write) spawn a real subagent via CLI or HTTP and wait for a real result — not a simulated search. Each call is framed by a server-side prompt template (`retrieve_prompt_template` / `write_prompt_template`, with prescriptive built-in defaults) so a fresh subagent immediately knows its job is memory retrieval/placement, not generic Q&A. |
| 7 | **Triple transport: MCP (streamable-http + legacy SSE) + OpenAPI** | Streamable-http for Claude Code and modern MCP clients, legacy SSE for Claude Desktop and other older clients, OpenAPI REST for ChatGPT Actions / curl / plain HTTP. Same tool logic, three doors — both MCP transports share the same `MCPServer` so tool registrations, auth, and instructions are identical across the surface. |
| 8 | **Audit everything** | Every tool call is logged with caller, tool, args, timing, result size. No silent access. |

## 3. Architecture

```
┌──────────────────────────────────────────────────┐
│  Clients (Claude Code, Claude Desktop, ChatGPT,  │
│  curl, etc.)                                      │
└─────┬────────────────┬────────────────┬──────────┘
      │ MCP streamable │ MCP legacy SSE │ REST / OpenAPI
      │ /mcp           │ /sse, /message │ /api/pf_*
      ▼                ▼                ▼
┌──────────────────────────────────────────────────┐
│  pagefault server (Go + mcp-go)                   │
│  ┌──────────┐  ┌───────────────────────────┐     │
│  │ Auth     │  │ Tool Dispatcher           │     │
│  │ (bearer  │  │  pf_maps  pf_peek         │     │
│  │  /header │  │  pf_load  pf_fault ─► sa  │     │
│  │  /none)  │  │  pf_scan  pf_ps           │     │
│  │          │  │  pf_poke  (direct / agent)│     │
│  └──────────┘  └───────────┬───────────────┘     │
│                            │                      │
│  ┌─────────────┐  ┌───────┴────────┐             │
│  │ Filters     │  │ Backend Registry│             │
│  │ (allow/deny │  │  filesystem     │             │
│  │  +write_*   │  │  subprocess     │             │
│  │  /redact/   │  │  http           │             │
│  │  tags)      │  │  subagent-cli   │             │
│  │ —optional—  │  │  subagent-http  │             │
│  └─────────────┘  │  (WritableBackend│             │
│                   │   implemented by │             │
│                   │   filesystem)    │             │
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
4. **Pre-filter**: path/tag allowlist + denylist (read calls use
   `AllowURI`; `pf_poke` direct mode uses `AllowWriteURI` with
   write-specific globs falling back to read allow/deny when unset)
5. Backend executes the query (read file, run subprocess, spawn
   subagent, or — for `pf_poke` direct — type-assert to
   `WritableBackend` and call `Write`, which enforces `write_paths`)
6. **Post-filter**: content redaction (if enabled; read path only)
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

A special backend type that spawns a full agent process, gives it a task, and returns its final response. This is what makes `pf_fault` (retrieve) and `pf_poke` mode:agent (write) powerful — the subagent has its own tools and reasons about the task rather than pattern-matching against a corpus.

```go
// SubagentBackend extends Backend with agent spawning capability.
type SubagentBackend interface {
    Backend
    Spawn(ctx context.Context, req SpawnRequest) (string, error)
    ListAgents() []AgentInfo
}

// SpawnRequest carries everything a backend needs to run one
// agent turn: what to do (task + purpose), who should do it
// (agent id), how long it can take (timeout), and optional hints
// (time range for retrieve, target for write).
type SpawnRequest struct {
    AgentID   string
    Task      string          // raw caller content; backend wraps with prompt template
    Purpose   SpawnPurpose    // "retrieve" | "write"
    TimeRange string          // optional, interpreted by subagent via {time_range}
    Target    string          // optional, interpreted via {target}
    Timeout   time.Duration
}

type AgentInfo struct {
    ID          string `json:"id"`
    Description string `json:"description"`
}
```

Before dispatch, the backend wraps `req.Task` with a resolved prompt template (per-agent override → per-backend default → built-in `DefaultRetrievePromptTemplate`/`DefaultWritePromptTemplate`). This is the layer that teaches a fresh subagent "you are a memory-retrieval/memory-write agent, do not fall back to your own world knowledge" — without it, a naive subagent answers from training data and `pf_fault` degrades to a generic Q&A passthrough.

Built-in implementations:
- **`subagent-cli`**: Runs a shell command, waits for stdout. Configurable command template with `{agent_id}` and `{task}` placeholders; `{task}` receives the *prompt-wrapped* task, not the raw caller string.
- **`subagent-http`**: POSTs to an HTTP endpoint, waits for JSON response. Configurable URL template and headers. `{task}` in the body template is JSON-escaped after the prompt wrap, so multi-line default templates survive the round-trip.

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

The server wraps the caller's raw `query` with the backend's configured `retrieve_prompt_template` (falling back to `DefaultRetrievePromptTemplate`) before dispatching it to the subagent, so the agent sees explicit memory-retrieval framing instead of a bare string. This is what separates pagefault from a generic Q&A middleman.

**Input:**
- `query` (string, required) — what to find / understand. Plain user question; do NOT rephrase as "search for X", the template already frames it.
- `agent` (string, optional) — which agent to spawn (default: first configured subagent)
- `timeout_seconds` (int, optional, default 120) — max wait time
- `time_range_start` (string, optional) — free-form earliest date/time hint passed through to the subagent via the template's `{time_range}` placeholder
- `time_range_end` (string, optional) — free-form latest date/time hint

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

**Agent-mode framing.** In `mode: "agent"`, pagefault wraps the raw content with the backend's `write_prompt_template` (default: `DefaultWritePromptTemplate`) before dispatching — the subagent sees "you are a memory-write agent, inspect the existing layout, persist the content at the most appropriate location, report the path(s) you wrote to" rather than a bare content string. Same three-layer precedence as retrieval: agent override → backend default → built-in.

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

> **Note (0.5.1):** `targets_written` is reserved in the response
> schema but is currently always absent — pagefault forwards the
> subagent's textual reply via `result` and has no structured way to
> extract the list of URIs the subagent wrote to. Populating
> `targets_written` requires the subagent to emit a structured
> response envelope; that work is deferred to Phase 5.

**Error cases:**
- Backend is not writable → `403 AccessViolation: backend is read-only`
- URI not in `write_paths` allowlist → `403 AccessViolation: write path not allowed`
- Content exceeds `max_entry_size` → `413 ContentTooLarge: entry exceeds max_entry_size`
- `format: "raw"` but `write_mode` is `"append"` → `400 InvalidRequest: raw format requires write_mode: any`
- Subagent times out (agent mode) → `200 OK` with `timed_out: true` in the response envelope — timeouts are flattened into a success envelope so clients can inspect the partial text instead of branching on an error code. Mirrors `pf_fault`. The `504 subagent_timeout` status code is reserved for future use (if pagefault ever wants to hard-fail instead of returning partial results) but is not currently emitted.

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

### MCP streamable-http (primary modern)

- Endpoint: `POST /mcp` (and method-agnostic `/mcp/*`)
- Tools are registered as MCP tools using mcp-go tool definitions with JSON Schema
- mcp-go handles protocol details (session management, JSON-RPC, SSE streaming inside the single connection)
- Auth: Bearer token in `Authorization` header (standard MCP auth pattern)
- Used by Claude Code and other modern streamable-http clients

### MCP legacy SSE (Claude Desktop, etc.)

- Endpoints: `GET /sse` (persistent event stream) + `POST /message?sessionId=…` (client → server messages)
- Mounted by default; toggle via `server.mcp.sse_enabled: false` if unused
- `GET /sse` returns `text/event-stream`, emits an initial `endpoint` event carrying the message URL + `sessionId`, then streams JSON-RPC responses back as `message` events
- `POST /message` returns `202 Accepted` and dispatches through the same `MCPServer`; the response comes back on the SSE stream from the paired GET
- Auth: same bearer token is sent on both the SSE GET and subsequent message POSTs — both paths sit behind the same auth middleware
- Used by Claude Desktop (as of 2026-04 it still only speaks the older SSE MCP wire format); the 0.6.0 native SSE transport removes the need for a `supergateway` or similar bridge

Both MCP transports share a single `MCPServer`, so tool registrations, server instructions (`mcpserver.WithInstructions`), and handler dispatch are identical — only the wire framing differs.

### Server-level instructions (MCP `initialize` response)

pagefault advertises a prescriptive instructions string on every `initialize` response. MCP clients typically surface this text in the agent's system prompt, which is the single most reliable lever for teaching agents *when* to reach for `pf_*` tools vs the built-ins.

- Default source: `internal/tool/instructions.go` → `DefaultInstructions`
- Override: `server.mcp.instructions` in the YAML config replaces the default verbatim
- Covers both "call pf_* when …" (signal phrases) and "do NOT call pf_* for …" (world knowledge, current-repo code) so eager agents don't over-trigger

### OpenAPI / REST (secondary)

- Endpoints: `POST /api/{tool_name}` for each tool
- OpenAPI 3.1 spec at `GET /api/openapi.json` (public, no auth — spec itself declares BearerAuth)
- Used by ChatGPT Custom GPT Actions, curl, and any plain HTTP client
- Auth: Same as MCP (Bearer token in header)

All three transports dispatch to the **same** pure `HandleX` functions via the `ToolDispatcher` — zero logic duplication; filter, audit, and error semantics are identical regardless of which door the client came through.

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

### Phase 4 — Writeback (Read-Write) ✅ (shipped in 0.5.0, bugfix in 0.5.1)

`pf_poke` ships as the write counterpart to `pf_peek` — two modes
(`direct` filesystem append and `agent` subagent delegation) guarded
by five independent gates (tool enable, server-wide write filter,
per-backend `Writable()`, per-backend `write_paths`, and
`max_entry_size`). A new `internal/write` package holds the
flock-serialised append primitive and the entry-template formatter;
the filesystem backend implements the optional
`backend.WritableBackend` interface; the filter pipeline gains
`AllowWriteURI` with write-specific allow/deny globs; and
`dispatcher.Write` routes the mutation path (filter → scheme →
type-assert → call). Agent mode composes a natural-language task and
delegates through `dispatcher.DeepRetrieve`, flattening timeouts into
a success envelope with `timed_out: true`. 0.5.1 fixed a bug where
`max_entry_size` was enforced against the *wrapped* body, silently
penalising `format: "entry"` callers — the check now runs in the
tool layer against the raw caller content, as always documented.

See `CHANGELOG.md` §0.5.0 (ship) and §0.5.1 (bugfix + doc drift pass)
for the detailed breakdowns, and `docs/api-doc.md` §pf_poke for the
tool reference.

### Phase 4.5 — Discoverability + UX pass ✅ (shipped in 0.6.0)

Unplanned work driven by real-deployment feedback. Three waves of
trace review surfaced that Phases 1-4 were *wire-correct* but
*discoverability-broken* — the tools existed, but cold agents did
not know when to reach for them, and even when they did, the
per-parameter descriptions and subagent prompts were vague enough
to produce wrong answers on live queries. No new tools; no
backend types; no breaking wire changes. Just text, tests, and
one internal interface refactor to unblock future knobs.

**Delivered in 0.6.0:**

- **Native MCP legacy-SSE transport** (`/sse` + `/message`) so
  Claude Desktop connects without an `npx supergateway` bridge.
  `server.mcp.sse_enabled` opt-out toggle. Shares the same
  MCPServer / tool registry / auth chain as `/mcp`.
- **Server-level MCP instructions** via `WithInstructions`.
  `internal/tool/instructions.go` holds a prescriptive default
  that covers: chat-history framing ("past conversations live
  here too"), a **Core rule** forbidding "I don't remember"
  without a pagefault check, English + Chinese signal phrase
  examples, temporal-reference routing, multi-agent routing
  (MUST call `pf_ps` first in multi-agent setups), and a 120s
  timeout floor for `pf_fault` / `pf_poke` mode:agent. Operator
  override via `server.mcp.instructions` is a full replace
  (documented worked example in `docs/config-doc.md`).
- **Server-side subagent prompt templates** (retrieve + write).
  `internal/backend/prompt.go` introduces `SpawnRequest`,
  `SpawnPurpose`, `ResolvePromptTemplate`, `WrapTask`, and the
  two default templates. Three-layer precedence: per-agent →
  per-backend → built-in. Every subagent backend wraps the raw
  caller content with the resolved template before substituting
  into its command / body template, so a fresh subagent is
  framed as a memory retriever / placer rather than a generic
  Q&A bot. This is the fix for the "wocha returned a toxicity
  sheet for oleander" failure mode.
- **`pf_fault` time range.** `time_range_start` /
  `time_range_end` optional free-form string parameters (CLI:
  `--after` / `--before`). Pagefault does not parse the values
  — it formats them into a hint line and passes through to the
  subagent via the template's `{time_range}` placeholder.
- **Dispatcher `DelegateWrite`** — write-side twin of
  `DeepRetrieve`. `pf_poke` mode:agent now routes through this
  instead of tunneling through `DeepRetrieve`, so the subagent
  picks up the write-framed prompt template (not the retrieve
  one).
- **Per-parameter tool descriptions rewritten** across every
  `pf_*` tool with concrete "how to construct" guidance and
  examples. `pf_scan.query` explains the grep semantics;
  `pf_peek.uri` warns against reconstructing URIs; `pf_fault.query`
  tells the agent it does NOT need to rephrase the user's question.
- **Agent-selection guidance** prescribing `pf_ps` → pick-by-description
  in multi-agent setups rather than relying on the "first configured"
  fallback. Reflected in `pf_fault.agent`, `pf_poke.agent`, `pf_ps`'s
  description, and `DefaultInstructions`.
- **Timeout-floor guidance** (never below 120s) across
  `pf_fault.timeout_seconds`, `pf_poke.timeout_seconds`, CLI
  `--timeout`, docs, and instructions.

Breaking interface change: `backend.SubagentBackend.Spawn` now
takes a `SpawnRequest` struct rather than `(agentID, task, timeout)`
positional args. Internal-only; all call sites updated in the
same release. Future knobs (caller context, tool-call budgets,
tracing ids) can be added to the struct without another
signature change.

See `CHANGELOG.md` §0.6.0 for the full breakdown. `docs/config-doc.md`
has a worked `server.mcp.instructions` override example showing
how to layer installation-specific framing on top of the default.

### Phase 4.6 — Keepalive hotfix + OAuth2 ✅ (shipped in 0.6.1 + 0.7.0)

Two tightly-linked releases driven by one Claude Desktop deployment
goal: make the native SSE MCP config reach pagefault directly, with
no `supergateway` bridge and no premature idle-timeout deaths on
long `pf_fault` calls. The fix split along the two layers the
problem lived on.

**0.6.1 — SSE keepalive pings.** The persistent `GET /sse` stream
no longer sits silent for the duration of a long subagent wait;
mcp-go's built-in keepalive is enabled with a 15-second interval
(tunable via `server.mcp.sse_keepalive` / `sse_keepalive_interval_seconds`),
so intermediate proxies see a `ping` event on a ticker and do not
close the connection. Resolves the "几十秒就挂" failure mode
reported against the live deployment. Note: operators on the
`supergateway → /mcp` bridge are not covered because mcp-go's
streamable-http transport has no per-request keepalive — bump the
reverse proxy's `proxy_read_timeout` / equivalent to 300s+ as a
workaround on that path.

**0.7.0 — OAuth2 client_credentials auth provider.** Claude
Desktop's built-in SSE credential UI only exposes Client ID and
Client Secret fields, so even with the keepalive fix a
bearer-authenticated pagefault deployment could not be reached
natively. 0.7.0 adds `auth.mode: "oauth2"` with the full minimum
viable client_credentials surface:

- **`internal/auth/oauth2.go`** — `OAuth2Provider` implementing
  `AuthProvider`, backed by a JSONL clients registry with
  bcrypt-hashed secrets and an in-memory access token store with
  configurable TTL (default 3600s). `IssueToken` verifies
  credentials via `bcrypt.CompareHashAndPassword`, issues a
  32-byte opaque token (prefix `pf_at_`), and intersects the
  caller-requested scopes with the client's allowed set.
  `Authenticate` checks issued tokens first and falls back to
  the legacy `BearerTokenAuth` when `bearer.tokens_file` is also
  configured — the **compound-mode** path that lets operators
  migrate Claude Desktop without breaking Claude Code.
- **`internal/server/oauth2.go`** — three new public HTTP
  endpoints: `GET /.well-known/oauth-protected-resource`
  (RFC 9728), `GET /.well-known/oauth-authorization-server`
  (RFC 8414), and `POST /oauth/token` (RFC 6749 §4.4). The
  discovery endpoints advertise only what pagefault supports:
  `grant_types_supported: ["client_credentials"]`,
  `token_endpoint_auth_methods_supported: ["client_secret_basic",
  "client_secret_post"]`, empty `response_types_supported`. The
  token endpoint parses credentials from either HTTP Basic
  (§2.3.1) or form body (§2.3.2), with the RFC 6749 §5.2 error
  envelope on failure (401 + WWW-Authenticate on Basic failures,
  400 on form-body failures). Mounted outside the auth middleware
  so clients can bootstrap before they have a token.
- **`cmd/pagefault/oauth_client.go`** — new CLI subcommand
  mirroring `pagefault token`. `create` generates a Client ID
  (slugified from `--label` or given via `--id`) and a 32-byte
  random Client Secret (prefix `pf_cs_`), hashes the secret with
  bcrypt, and prints both exactly once. `ls` lists ID + label +
  scopes + created timestamp (never the hash or the secret).
  `revoke` removes future issuance for that client but warns
  that already-issued access tokens remain valid until TTL or
  server restart.
- **`internal/config/config.go`** — `AuthConfig.Mode` oneof
  validator extended with `"oauth2"`; new `OAuth2Config` struct
  with `clients_file`, `issuer`, `access_token_ttl_seconds`,
  `default_scopes` plus `AccessTokenTTLOrDefault` /
  `DefaultScopesOrDefault` helpers.
- **Issuer resolution** in the discovery handlers: explicit
  `auth.oauth2.issuer` → `server.public_url` → inferred from
  incoming request's scheme + host (honouring `X-Forwarded-Proto`
  and `X-Forwarded-Host`).

Tests: `internal/auth/oauth2_test.go` (13 unit tests covering
issue, authenticate, expire + sweep, compound fallback, scope
intersection, reload, parser edge cases, entropy),
`internal/server/oauth2_test.go` (11 integration tests via
httptest: discovery shape, Basic + form-body + invalid +
unsupported-grant + missing-creds paths, end-to-end token →
`/api/pf_maps`, compound-mode legacy bearer, unmounted when
disabled, URL-encoded Basic credential decoding),
`cmd/pagefault/oauth_client_test.go` (full CLI lifecycle plus
stored-hash-verifies-printed-secret round-trip).

Docs: `docs/config-doc.md` §auth → "mode: oauth2 — client_credentials
grant (shipped in 0.7.0)" with a complete worked example for
Claude Desktop; `docs/security.md` §Auth → "OAuth2 client_credentials
(0.7.0+)" covering bcrypt/`crypto/rand`/TTL/compound-mode/revocation
semantics; `docs/api-doc.md` §Authentication → "OAuth2
client_credentials (0.7.0+)" with the POST /oauth/token wire
format; `configs/example.yaml` gains a commented-out oauth2 +
compound block; `README.md` Claude Desktop section rewritten to
lead with the native OAuth2 path and keep `supergateway` as a
fallback.

Deliberately out of scope for 0.7.0: authorization_code flow,
PKCE, refresh tokens, dynamic client registration, per-scope
tool ACLs, and persistent token storage. The target workload
(Claude Desktop's static client_id/client_secret UI) does not
need any of them.

See `CHANGELOG.md` §0.6.1 and §0.7.0 for the full per-release
breakdown.

### Phase 5 — Hardening

1. Caching layer (LRU in-process, or Redis)
2. Streaming for long subagent responses
3. Metrics endpoint (Prometheus)
4. Docker image
5. systemd unit file example
6. Update all docs
7. Version bump + CHANGELOG

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

## 14. For Contributors: Where to Start

Phases 1–4 have shipped. For new work, read **`CLAUDE.md`** for the
directory tree / build commands / per-task recipes, then
**`CHANGELOG.md`** for what just landed. The phase sections in §10
above list what each release delivered and where to find the
detailed notes.

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
