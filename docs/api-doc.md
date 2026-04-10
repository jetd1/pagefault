# pagefault — API Reference (Phase 1)

pagefault exposes its tools over two transports:

- **MCP** (streamable-http): `POST /mcp`. For Claude-family clients. Tools are
  registered via mcp-go and return JSON payloads wrapped in a single text
  content block.
- **REST**: `POST /api/{tool_name}`. For curl, ChatGPT Custom GPTs, or any
  other HTTP client. Request body is JSON; response body is JSON.

Both transports dispatch to the same handler, so the inputs and outputs are
identical.

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

## `list_contexts`

Returns every configured context with its name and description. Zero-cost —
no backend calls.

**Endpoint:** `POST /api/list_contexts`

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

## `get_context`

Load and return a pre-composed context by name. The dispatcher reads each
source file, applies the filter pipeline, concatenates the content with
markdown separators, and truncates if the configured `max_size` is exceeded.

**Endpoint:** `POST /api/get_context`

**Request:**

| Field    | Type    | Required | Default     | Notes |
|----------|---------|----------|-------------|-------|
| `name`   | string  | yes      | —           | Context name (see `list_contexts`). |
| `format` | string  | no       | `markdown`  | `markdown` or `json`. Phase 1 only implements `markdown`. |

**Response:**

```json
{
  "name": "demo",
  "format": "markdown",
  "content": "# memory://README.md\n\n...\n\n---\n\n# memory://notes.md\n\n..."
}
```

If the content is larger than the context's `max_size`, it is truncated and
`"...[truncated]"` is appended.

**Errors:** 400 (missing name), 404 (unknown context).

---

## `search`

Runs a query across one or more backends and returns ranked results.
Phase-1's filesystem backend uses case-insensitive substring matching and
returns the first match per file.

**Endpoint:** `POST /api/search`

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

## `read`

Read a resource by URI. The URI's scheme (`memory://`, etc.) determines which
backend handles the request.

**Endpoint:** `POST /api/read`

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

- `deep_retrieve` — spawn a subagent to do comprehensive retrieval (Phase 2)
- `list_agents` — list configured subagents (Phase 2)
- `write` — direct append + agent writeback (Phase 4)
