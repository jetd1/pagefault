# pagefault — API Reference (Phase 1–4)

pagefault exposes its tools over two transports:

- **MCP** (streamable-http): `POST /mcp`. For Claude-family clients. Tools are
  registered via mcp-go and return JSON payloads wrapped in a single text
  content block.
- **REST**: `POST /api/{tool_name}`. For curl, ChatGPT Custom GPTs, or any
  other HTTP client. Request body is JSON; response body is JSON.

Both transports dispatch to the same handler, so the inputs and outputs are
identical.

## Tool naming

pagefault tools use a `pf_` prefix and borrow vocabulary from Unix memory
management / kernel debugging. The shipped tools:

| Wire name  | What it does                                    | Metaphor                   | Phase |
|------------|-------------------------------------------------|----------------------------|-------|
| `pf_maps`  | List pre-composed memory regions (contexts)     | `/proc/<pid>/maps`         | 1     |
| `pf_load`  | Load a region into working memory               | Page swap-in               | 1     |
| `pf_scan`  | Scan backends for content matching a query      | `kswapd`-style page scan   | 1     |
| `pf_peek`  | Read a specific resource (optionally sliced)    | Debugger `PEEKDATA`        | 1     |
| `pf_fault` | Spawn a subagent for deep retrieval              | Real page fault handler    | 2     |
| `pf_ps`    | List configured subagents                        | `ps(1)`                    | 2     |
| `pf_poke`  | Poke content back into memory (append / agent)   | Debugger `POKEDATA`        | 4     |

See `plan.md` §10 for the full roadmap.

## CLI

Every tool is also available as a subcommand of the `pagefault` binary,
so you can drive it locally without starting the HTTP server. The CLI
form drops the `pf_` prefix (the outer `pagefault` binary already
provides the namespace):

```
pagefault maps                 — list configured memory regions
pagefault load <name>          — load an assembled region to stdout
pagefault scan <query...>      — scan backends for a query
pagefault peek <uri>           — read a resource by URI
pagefault fault <query...>     — spawn a subagent for deep retrieval
pagefault ps                   — list configured subagents
pagefault poke [--mode direct|agent] [--uri URI] <content...>
                               — poke content back into memory
```

**Common flags** (accepted by every tool subcommand):

| Flag              | Default  | Notes                                                      |
|-------------------|----------|------------------------------------------------------------|
| `--config <path>` | see below | path to `pagefault.yaml`                                  |
| `--no-filter`     | off      | bypass the filter pipeline (operator escape hatch)         |
| `--json`          | off      | emit machine-readable JSON instead of tabwriter/raw text   |

**Per-command flags:**

- `load`: `--format markdown|markdown-with-metadata|json` (overrides the context's configured format)
- `scan`: `--limit N` (default 10), `--backends a,b` (comma-separated names to restrict to)
- `peek`: `--from N`, `--to N` (1-indexed, inclusive line range)
- `fault`: `--agent <id>` (which subagent to spawn; default is the first configured), `--timeout N` (seconds; default 120)
- `ps`: *(no extra flags)*
- `poke`: `--mode direct|agent` (default `direct`), `--uri <uri>` (required for `direct`), `--format entry|raw` (default `entry`), `--agent <id>` (mode:agent only), `--target <hint>` (mode:agent only; default `auto`), `--timeout N` (mode:agent only; seconds; default 120). Content is taken from positional args, or from stdin if no positional args are provided — so `echo "fixed auth bug" | pagefault poke --mode direct --uri memory://notes/today.md` works.

**Config lookup order:**

1. explicit `--config <path>` flag
2. `$PAGEFAULT_CONFIG` environment variable
3. `./pagefault.yaml` in the current directory

If none resolve to a readable file, the command exits non-zero with a
clear error.

**Semantics shared with HTTP:**

- Same dispatcher, same `HandleX` functions, so the CLI sees exactly
  what an MCP client would see — filter pipeline, audit logging, and
  error mapping are identical.
- Filters apply by default. `--no-filter` is the explicit operator
  override when you need to inspect something the filters are hiding.
- Every CLI call is audit-logged (caller identity is `cli` / `pagefault
  CLI`). If `audit.mode: stdout` is set in the config, CLI invocations
  rewrite it to `stderr` so the data stream stays pipe-clean.
- Positional arguments can appear anywhere on the command line; a local
  flag-hoisting helper means `pagefault peek memory://foo.md --from 5`
  works the same as `pagefault peek --from 5 memory://foo.md`.

**Examples:**

```bash
# List what's configured
pagefault maps --config pagefault.yaml

# Assemble a context as markdown
pagefault load demo --config pagefault.yaml

# Search, restricted to one backend, JSON for piping
pagefault scan "memory leak" --config pagefault.yaml --backends fs --json \
  | jq -r '.results[].uri'

# Read lines 10-20 of a file
pagefault peek memory://notes/2026-04-10.md --config pagefault.yaml --from 10 --to 20

# List configured subagents
pagefault ps --config pagefault.yaml

# Spawn an agent for deep retrieval
pagefault fault "what did I decide about auth last month?" \
  --config pagefault.yaml --agent wocha --timeout 180

# Environment-variable config (no --config flag needed)
export PAGEFAULT_CONFIG=~/.config/pagefault/pagefault.yaml
pagefault maps
```

## Authentication

All tool endpoints require authentication unless `auth.mode: none` is set in
the config. The default mode is `bearer`:

```
Authorization: Bearer pf_xxx...
```

`/health` and `/` are public (no auth required).

## Common response shapes

Success: HTTP 200 with a JSON object.

Errors use a single structured envelope with a stable `code` (snake_case)
that clients can branch on without parsing `message`:

```json
{
  "error": {
    "code": "invalid_request",
    "status": 400,
    "message": "invalid request: name is required"
  }
}
```

| HTTP status | Code                  | Typical cause |
|-------------|-----------------------|---------------|
| 400         | `invalid_request`     | Missing or malformed field, empty query, bad JSON body. |
| 401         | `unauthenticated`     | Missing, unknown, or revoked bearer token. |
| 403         | `access_violation`    | URI blocked by filter; untrusted proxy IP. |
| 404         | `resource_not_found`  | `pf_peek` URI does not exist. |
| 404         | `context_not_found`   | `pf_load` name does not exist. |
| 404         | `backend_not_found`   | `pf_scan` named an unknown backend, or `pf_peek` URI uses an unknown scheme. |
| 404         | `agent_not_found`     | `pf_fault` / `pf_poke` mode:agent named an unconfigured agent. |
| 413         | `content_too_large`   | `pf_poke` mode:direct content exceeded the target backend's `max_entry_size`. |
| 429         | `rate_limited`        | Caller exceeded `server.rate_limit` budget. Response includes a `Retry-After` header. |
| 502         | `backend_unavailable` | Backend network error, missing directory, misconfigured HTTP `response_path`. |
| 504         | `subagent_timeout`    | Reserved for direct surfacing; `pf_fault` / `pf_poke` mode:agent normally flatten timeouts to `timed_out: true` in a 200 response. |
| 500         | `internal_error`      | Unexpected internal failure. |

---

## `pf_maps`

Returns every configured memory region (context) with its name and
description. Zero-cost — no backend calls.

**Endpoint:** `POST /api/pf_maps`

**Request body:** none (empty `{}` is accepted)

**Response:**

```json
{
  "contexts": [
    {"name": "user-profile", "description": "User's personal profile and setup"},
    {"name": "recent-activity", "description": "Daily notes from the last N days"}
  ]
}
```

---

## `pf_load`

Load a named memory region (context) into working memory. The dispatcher
reads each source file, applies the filter pipeline, concatenates the content
with markdown separators, and truncates if the configured `max_size` is
exceeded.

**Endpoint:** `POST /api/pf_load`

**Request:**

| Field    | Type    | Required | Default     | Notes |
|----------|---------|----------|-------------|-------|
| `name`   | string  | yes      | —           | Region name (see `pf_maps`). |
| `format` | string  | no       | `markdown`  | `markdown`, `markdown-with-metadata`, or `json`. Overrides the context's configured default. |

**Response:**

```json
{
  "name": "demo",
  "format": "markdown",
  "content": "# memory://README.md\n\n...\n\n---\n\n# memory://notes.md\n\n...",
  "skipped_sources": [
    {"uri": "memory://secrets.md", "reason": "blocked by uri filter"}
  ]
}
```

### Format behaviour

- **`markdown`** (default) — each source is rendered as `# {uri}` followed by
  its body, with `\n\n---\n\n` separators. Truncated at the nearest UTF-8
  rune boundary (so multi-byte characters are never split) with
  `"...[truncated]"` appended when the joined output exceeds `max_size`.
- **`markdown-with-metadata`** — same layout, but each header is followed by
  a blockquote summarizing `content-type` and `tags` so downstream models
  can see the backend-level provenance without a separate call.
- **`json`** — the `content` field is a JSON-encoded bundle:
  ```json
  {"name":"demo","sources":[{"uri":"memory://a.md","content_type":"text/markdown","content":"...","tags":["notes"],"metadata":{}}]}
  ```
  `max_size` enforcement in JSON mode drops sources from the tail rather
  than byte-truncating (so the emitted document is always valid JSON).
  Dropped sources appear in `skipped_sources` with
  `reason: "max_size budget exceeded"`.

### Skipped sources

If one or more configured sources were dropped (blocked by a filter, backend
read failure, JSON `max_size` budget), they are listed in `skipped_sources`
with a human-readable reason. The field is omitted entirely when nothing was
skipped. Each skip is also logged at `WARN` level with the context name,
URI, and reason.

**Errors:** 400 (missing name, unknown format), 404 (unknown context).

---

## `pf_scan`

Scans configured backends for content matching a query and returns ranked
results. Phase-1's filesystem backend uses case-insensitive substring
matching and returns the first match per file.

**Endpoint:** `POST /api/pf_scan`

**Request:**

| Field        | Type      | Required | Default | Notes |
|--------------|-----------|----------|---------|-------|
| `query`      | string    | yes      | —       | Search query. |
| `limit`      | int       | no       | 10      | Maximum number of results. |
| `backends`   | string[]  | no       | all     | Restrict to specific backend names. |
| `date_range` | object    | no       | —       | `{"from":"YYYY-MM-DD","to":"YYYY-MM-DD"}` — accepted for forward compatibility, ignored by Phase-1 backends. |

**Response:**

```json
{
  "results": [
    {
      "uri": "memory://2026-04-10.md",
      "snippet": "...matched text...",
      "score": null,
      "metadata": {"backend": "fs", "line": 12, "tags": ["daily"]},
      "backend": "fs"
    }
  ]
}
```

Results are interleaved across backends (no cross-backend ranking in Phase 1).
Each result's `backend` field is the originating backend name.

**Errors:** 400 (empty query), 404 (unknown backend in `backends`).

---

## `pf_peek`

Peek at a specific resource by URI, optionally slicing a line range. The
URI's scheme (`memory://`, etc.) determines which backend handles the
request.

**Endpoint:** `POST /api/pf_peek`

**Request:**

| Field        | Type    | Required | Default | Notes |
|--------------|---------|----------|---------|-------|
| `uri`        | string  | yes      | —       | Resource URI (e.g. `memory://2026-04-10.md`). |
| `from_line`  | int     | no       | —       | Start line (1-indexed, inclusive). |
| `to_line`    | int     | no       | —       | End line (1-indexed, inclusive). |

Both line bounds are optional and may be used independently.

**Response:**

```json
{
  "resource": {
    "uri": "memory://2026-04-10.md",
    "content": "# ... full file or line slice ...",
    "content_type": "text/markdown",
    "metadata": {
      "backend": "fs",
      "size": 1234,
      "mtime": "2026-04-10T12:00:00Z",
      "tags": ["daily", "notes"]
    }
  }
}
```

**Errors:** 400 (missing/invalid URI), 403 (blocked by filter), 404 (unknown
scheme or resource not found).

---

## `pf_fault`

The real page fault. Spawns a subagent (a full external process with its
own tool access) to carry out a natural-language retrieval task and
returns the agent's final response. Use when `pf_scan` / `pf_peek` miss
and you need something smarter than substring matching.

**Endpoint:** `POST /api/pf_fault`

**Request:**

| Field             | Type   | Required | Default | Notes                                                                 |
|-------------------|--------|----------|---------|-----------------------------------------------------------------------|
| `query`           | string | yes      | —       | Natural-language query: what to find, understand, or synthesize.     |
| `agent`           | string | no       | first   | Subagent id to spawn (see `pf_ps`). Empty picks the first configured. |
| `timeout_seconds` | int    | no       | 120     | Max seconds to wait for the agent. Also used as the kill deadline.    |

**Response (success):**

```json
{
  "answer": "The agent's synthesized response...",
  "agent": "wocha",
  "backend": "openclaw",
  "elapsed_seconds": 47.3
}
```

**Response (timeout):** timeouts are NOT HTTP errors — the structured
response carries a `timed_out` flag and any partial output the backend
captured before the kill:

```json
{
  "agent": "wocha",
  "backend": "openclaw",
  "elapsed_seconds": 120.0,
  "timed_out": true,
  "partial_result": "...text captured before the deadline, if any..."
}
```

**Errors:** 400 (empty query), 404 (unknown agent / no subagent backend
configured), 500 (backend spawn error).

---

## `pf_ps`

List every subagent exposed by every configured `SubagentBackend`. Zero
cost — agents are read from config, no process/network I/O.

**Endpoint:** `POST /api/pf_ps`

**Request body:** none (empty `{}` is accepted)

**Response:**

```json
{
  "agents": [
    {"id": "wocha", "description": "Dev agent with Feishu, LCM, workspace, and coding tools", "backend": "openclaw"},
    {"id": "main",  "description": "Primary personal agent with full tool access",             "backend": "openclaw"}
  ]
}
```

Each entry's `backend` is the name of the `SubagentBackend` that hosts
the agent. Multiple backends may expose agents with the same id — always
disambiguate via `backend` when that happens.

---

## Health

`GET /health` — returns overall status plus per-backend status. No auth
required. Always returns HTTP 200 so liveness probes don't need to branch
on status codes; orchestrators should read the envelope's top-level
`status` field instead.

```json
{
  "status": "ok",
  "version": "0.4.0",
  "backends": {
    "fs":       {"status": "ok"},
    "openclaw": {"status": "ok"}
  }
}
```

Per-backend `status` is one of:

- `"ok"` — backend implements the optional `HealthChecker` interface and
  the probe succeeded, or the backend does not implement `HealthChecker`
  (we have no better signal).
- `"unavailable"` — probe returned an error; a truncated error message is
  included in the `error` field on the same entry.

Top-level `status` is `"ok"` when every backend is ok, `"degraded"` when
at least one is unavailable but others are still ok, and `"unavailable"`
when every backend is unavailable.

The filesystem backend implements `HealthChecker` by stat'ing its
configured root; a deleted / unmounted root surfaces as `"unavailable"`
within a 2 second probe timeout.

## OpenAPI spec

`GET /api/openapi.json` — returns an OpenAPI 3.1.0 document describing
every *enabled* REST tool. The endpoint is public (no auth required) so
importers like ChatGPT Custom GPT Actions can fetch the schema before
a bearer token is supplied.

```
GET /api/openapi.json → 200 application/json
```

The document is generated live from the current config — the
`servers[0].url` field echoes `server.public_url`, paths are dropped for
disabled tools, and response schemas include the Phase-3 error envelope
(`ErrorEnvelope`) so clients can generate types for both success and
failure responses.

To connect a ChatGPT Custom GPT: **Actions → Import from URL →
`https://<your-pagefault>/api/openapi.json` → Authentication: Bearer →
paste your `pf_…` token**.

## `pf_poke`

Poke content back into memory — the write counterpart to `pf_peek`.
Two modes:

- **`direct`** — pagefault appends the content to a specific URI on a
  writable backend. Fast, deterministic, zero-token. The backend
  enforces its own `write_paths` allowlist, `write_mode` policy
  (append-only vs. any mutation), and `max_entry_size` cap.
- **`agent`** — pagefault spawns a subagent (the same machinery
  `pf_fault` uses) and hands it a natural-language instruction: "A
  remote agent wants to record X — read the relevant memory files,
  decide where to write it, and append." Trust is delegated to the
  subagent, which has its own workspace access.

**Endpoint:** `POST /api/pf_poke`

**Request:**

| Field             | Type   | Required           | Default   | Notes                                                                                                               |
|-------------------|--------|--------------------|-----------|---------------------------------------------------------------------------------------------------------------------|
| `content`         | string | yes                | —         | The content to persist. Applies to both modes.                                                                      |
| `mode`            | string | yes                | —         | `"direct"` or `"agent"`.                                                                                            |
| `uri`             | string | yes (mode:direct)  | —         | Target URI for direct append (e.g. `memory://notes/2026-04-11.md`).                                                 |
| `format`          | string | no (mode:direct)   | `entry`   | `"entry"` wraps the content as a timestamped markdown block; `"raw"` appends bytes unchanged (requires `write_mode: "any"`). |
| `agent`           | string | no (mode:agent)    | first     | Subagent id (see `pf_ps`). Empty picks the first configured.                                                         |
| `target`          | string | no (mode:agent)    | `auto`    | Free-form hint for the subagent (`"auto"`, `"daily"`, `"long-term"`, or any custom string).                        |
| `timeout_seconds` | int    | no (mode:agent)    | 120       | Per-call deadline for the subagent spawn.                                                                           |

**Response (mode:direct):**

```json
{
  "status": "written",
  "mode": "direct",
  "uri": "memory://notes/2026-04-11.md",
  "bytes_written": 142,
  "format": "entry",
  "backend": "fs"
}
```

The `bytes_written` count is what hit disk after entry-template
wrapping (it will be larger than `len(content)` in `format: "entry"`
mode because of the leading `\n---\n## [HH:MM] ...` header).

**Response (mode:agent):**

```json
{
  "status": "written",
  "mode": "agent",
  "agent": "wocha",
  "backend": "openclaw",
  "elapsed_seconds": 23.5,
  "result": "Appended to memory/2026-04-11.md under 'Bug fix' section."
}
```

Timeouts on mode:agent are flattened into a success envelope with
`"timed_out": true` and whatever stdout the subagent produced before
the deadline surfaced as `result` — same pattern as `pf_fault`.

The OpenAPI schema also advertises a `targets_written` field
(array of URIs the subagent reports writing). As of 0.5.1 pagefault
has no structured way to extract this from the subagent's reply, so
the field is **reserved but always absent**. Clients that need to
know which files were touched must parse the free-form `result`
text. Populating `targets_written` waits for a structured subagent
response envelope in Phase 5.

**Errors:**

- `400 invalid_request` — missing `content`/`mode`, unknown `mode`,
  `uri` missing in direct mode, `format: "raw"` against a backend
  whose `write_mode` is not `"any"`.
- `403 access_violation` — backend is not writable, URI not in the
  backend's `write_paths`, URI blocked by `filters.path.write_*`.
- `404 agent_not_found` — mode:agent named an unconfigured agent or
  no `SubagentBackend` is configured at all.
- `413 content_too_large` — content (raw, measured before
  entry-template wrapping) exceeded the backend's `max_entry_size`.
- `504 subagent_timeout` — not normally surfaced; see the
  `timed_out` flag on the success envelope instead.

**Security notes:**

- **Default is read-only.** A filesystem backend is only writable
  when `writable: true` is set.
- **Direct mode is sandboxed.** The backend enforces `write_paths`,
  `write_mode`, and symlink resolution for new files (so a
  parent-directory symlink cannot escape the backend root on first
  write). `max_entry_size` is enforced by the `pf_poke` tool layer
  against the raw caller content before entry-template wrapping
  (see `model.ErrContentTooLarge`).
- **Agent mode delegates trust.** pagefault's `write_paths` do
  *not* apply to what the subagent writes — the subagent has its
  own workspace, conventions, and guardrails. pagefault just
  forwards the instruction and captures the response. See
  `docs/security.md` §Write-side threat model for details.

---

## Planned (future phases)

Phases 1–4 are shipped. Phase 5 items on the roadmap (OAuth2,
caching, streaming, metrics) are tracked in `plan.md` §10. No
additional tool surface is planned for Phase 5.
