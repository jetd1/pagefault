# pagefault — API Reference (Phase 1)

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
management / kernel debugging. The short Phase-1 table:

| Wire name  | What it does                                    | Metaphor                         |
|------------|-------------------------------------------------|----------------------------------|
| `pf_maps`  | List pre-composed memory regions (contexts)     | `/proc/<pid>/maps`               |
| `pf_load`  | Load a region into working memory               | Page swap-in                     |
| `pf_scan`  | Scan backends for content matching a query     | `kswapd`-style page scan         |
| `pf_peek`  | Read a specific resource (optionally sliced)    | Debugger `PEEKDATA`              |

Later phases add `pf_fault` (deep retrieval — triggers a real subagent),
`pf_ps` (list configured subagents), and `pf_poke` (writeback, paired with
`pf_peek`). See `plan.md` for the full roadmap.

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
```

**Common flags** (accepted by all four):

| Flag              | Default  | Notes                                                      |
|-------------------|----------|------------------------------------------------------------|
| `--config <path>` | see below | path to `pagefault.yaml`                                  |
| `--no-filter`     | off      | bypass the filter pipeline (operator escape hatch)         |
| `--json`          | off      | emit machine-readable JSON instead of tabwriter/raw text   |

**Per-command flags:**

- `load`: `--format markdown|json` (overrides the context's configured format)
- `scan`: `--limit N` (default 10), `--backends a,b` (comma-separated names to restrict to)
- `peek`: `--from N`, `--to N` (1-indexed, inclusive line range)

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

Errors:

| Condition | HTTP status | Body |
|-----------|-------------|------|
| Missing/invalid JSON body | 400 | `{"error":"bad request","message":"..."}` |
| Missing required field    | 400 | `{"error":"bad request","message":"invalid request: ..."}` |
| Missing/invalid token     | 401 | `{"error":"unauthenticated","message":"..."}` |
| Blocked by filter         | 403 | `{"error":"forbidden","message":"access violation: ..."}` |
| Unknown resource/context  | 404 | `{"error":"not found","message":"..."}` |
| Backend unavailable       | 502 | `{"error":"bad gateway","message":"..."}` |
| Internal error            | 500 | `{"error":"internal server error","message":"..."}` |

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
| `format` | string  | no       | `markdown`  | `markdown` or `json`. Phase 1 only implements `markdown`. |

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

If the content is larger than the context's `max_size`, it is truncated at
the nearest UTF-8 rune boundary (so multi-byte characters are never split)
and `"...[truncated]"` is appended.

If one or more configured sources were dropped (blocked by a filter, backend
read failure), they are listed in `skipped_sources` with a human-readable
reason. The field is omitted entirely when nothing was skipped. Each skip is
also logged at `WARN` level with the context name, URI, and reason.

**Errors:** 400 (missing name), 404 (unknown context).

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

## Health

`GET /health` — returns overall status plus per-backend status. No auth
required.

```json
{
  "status": "ok",
  "version": "0.1.0",
  "backends": {"fs": "ok"}
}
```

## Planned (future phases)

These tools are defined in `plan.md` but **not implemented in Phase 1**:

- `pf_fault` — the real page fault. Spawns a subagent to do comprehensive
  retrieval from backing store (Phase 2).
- `pf_ps` — list configured subagents (Phase 2), `ps`-style.
- `pf_poke` — direct append + agent writeback, the write counterpart to
  `pf_peek` (Phase 4).
