# pagefault — Configuration Reference

pagefault is a runtime for a single YAML configuration file. Everything
(backends, contexts, auth, filters, tools, audit) lives in this file.

The loader expands `${ENV_VAR}` references in the file contents before
parsing, so secrets can be externalized.

## Top-level shape

```yaml
server:   { ... }   # HTTP listener
auth:     { ... }   # authentication layer
backends: [ ... ]   # data sources (required, at least one)
contexts: [ ... ]   # pre-composed bundles (optional)
tools:    { ... }   # per-tool enable/disable (optional)
filters:  { ... }   # optional filter pipeline
audit:    { ... }   # audit logging
```

A minimal working config is in `configs/minimal.yaml` (filesystem
backend only). For a tour of every backend type with inline docs, see
`configs/example.yaml`.

---

## `server`

```yaml
server:
  host: "127.0.0.1"     # required
  port: 8444            # required (1..65535)
  public_url: "https://pagefault.example.com"  # optional — advertised by /api/openapi.json
  cors:
    enabled: false
    allowed_origins: ["https://chat.openai.com"]
    allowed_methods: ["GET", "POST", "OPTIONS"]
    allowed_headers: ["Content-Type", "Authorization"]
    allow_credentials: false
    max_age: 600
  rate_limit:
    enabled: false
    rps: 10              # steady-state tokens per second per caller
    burst: 20            # bucket size per caller
```

| Field        | Type   | Default       | Notes |
|--------------|--------|---------------|-------|
| `host`       | string | `127.0.0.1`   | Bind address. |
| `port`       | int    | `8444`        | Listen port. |
| `public_url` | string | empty         | Advertised as the `servers[0].url` in `/api/openapi.json`. Falls back to `/` when unset. |

### `server.cors`

Opt-in cross-origin handling for the REST transport. When `enabled: false`
(default) no CORS headers are emitted and browsers reject cross-origin
requests — fine for loopback and same-origin deployments. Setting
`enabled: true` with an empty `allowed_origins` list is equivalent to
`enabled: false`.

| Field                    | Type     | Default                         | Notes |
|--------------------------|----------|---------------------------------|-------|
| `enabled`                | bool     | `false`                         | Master switch. |
| `allowed_origins`        | string[] | `[]`                            | Exact-match origin allowlist. Use `"*"` for any origin (downgraded to echo-mode when `allow_credentials: true`). |
| `allowed_methods`        | string[] | `[GET, POST, OPTIONS]`          | Preflight `Access-Control-Allow-Methods`. |
| `allowed_headers`        | string[] | `[Content-Type, Authorization]` | Preflight `Access-Control-Allow-Headers`. |
| `allow_credentials`      | bool     | `false`                         | Emits `Access-Control-Allow-Credentials: true`. |
| `max_age`                | int      | `600`                           | Preflight cache in seconds. |

### `server.rate_limit`

Per-caller token bucket applied after auth. Callers are keyed on their
resolved `caller.id` (token id for bearer auth, header value for
trusted-header auth, or literal `"anonymous"` for `mode: none`). When
`enabled: false` the middleware is a no-op.

| Field      | Type    | Default | Notes |
|------------|---------|---------|-------|
| `enabled`  | bool    | `false` | Master switch. |
| `rps`      | float   | `10`    | Steady-state refill rate (tokens/second). |
| `burst`    | int     | `20`    | Bucket size — maximum burst before throttling kicks in. |

A caller that exceeds its budget receives HTTP 429 with a
`Retry-After` header and the standard structured error envelope:

```json
{"error": {"code": "rate_limited", "status": 429, "message": "rate limit exceeded"}}
```

---

## `auth`

```yaml
auth:
  mode: "bearer"                        # "none" | "bearer" | "trusted_header"
  bearer:
    tokens_file: "/etc/pagefault/tokens.jsonl"
  trusted_header:
    header: "X-Forwarded-User"
    trusted_proxies: ["127.0.0.1"]
```

| Field                   | Type        | Required when        | Notes |
|-------------------------|-------------|----------------------|-------|
| `mode`                  | enum        | always               | `none`, `bearer`, or `trusted_header`. |
| `bearer.tokens_file`    | path        | `mode: bearer`       | JSONL file, one record per line. |
| `trusted_header.header` | string      | `mode: trusted_header` | Header name carrying the identity. |
| `trusted_header.trusted_proxies` | string[] | optional         | If set, the remote IP must be in this list. |

### Token file format (`bearer`)

One JSON object per line, blank lines and `#`-prefixed comment lines allowed:

```jsonl
# My tokens
{"id":"laptop","token":"pf_xxx","label":"Laptop","metadata":{"device":"macos"}}
{"id":"phone","token":"pf_yyy","label":"Phone"}
```

Tokens are managed with `pagefault token create / ls / revoke`.

---

## `backends`

At least one backend is required. Each entry has a unique `name` and a
`type`. Phase 1 ships `filesystem`; Phase 2 adds `subprocess`, `http`,
`subagent-cli`, and `subagent-http`. See each type-specific section
below for its required fields.

```yaml
backends:
  - name: fs
    type: filesystem
    root: "/home/jet/.openclaw/workspace"
    include: ["memory/**/*.md", "AGENTS.md"]
    exclude: ["memory/intimate.md"]
    uri_scheme: "memory"
    auto_tag:
      "memory/**/*.md": ["daily", "memory"]
      "AGENTS.md":      ["config", "bootstrap"]
    sandbox: true
```

### `filesystem` backend

| Field            | Type                | Required | Default  | Notes |
|------------------|---------------------|----------|----------|-------|
| `name`           | string              | yes      | —        | Unique backend identifier. |
| `type`           | string              | yes      | —        | Must be `filesystem`. |
| `root`           | path                | yes      | —        | Directory to serve. Resolved to an absolute path at startup. |
| `include`        | string[]            | no       | all      | Doublestar globs; files must match at least one to be visible. |
| `exclude`        | string[]            | no       | —        | Globs; files matching any are hidden. |
| `uri_scheme`     | string              | yes      | —        | URI scheme for this backend (e.g. `memory`). |
| `auto_tag`       | map[string][]string | no       | —        | Path glob → tag list. Tags become resource metadata and are visible to the tag filter. |
| `sandbox`        | bool                | no       | `false`  | If true, reject any file whose resolved path (after symlink resolution) escapes `root`. |
| `writable`       | bool                | no       | `false`  | Phase 4. Set to `true` to enable `pf_poke` against this backend. Every other write field below is ignored when this is false. |
| `write_paths`    | string[]            | no       | `[]`     | Phase 4. Doublestar URI globs that accept writes. **Patterns must include the URI scheme** (e.g. `memory://memory/*.md`), unlike `include` which matches against relative paths — a scheme-less `notes/*.md` here silently matches nothing. Empty means "every URI that passes `include`", which is rarely what you want. |
| `write_mode`     | string              | no       | `append` | Phase 4. `append` (default, safest) or `any`. As of 0.5.1 the only observable effect of `any` is unlocking `format: "raw"` on `pf_poke`; prepend and overwrite operations are reserved but not yet implemented. Validated at config load. |
| `max_entry_size` | int                 | no       | `2000`   | Phase 4. Max bytes per `pf_poke` payload, measured on the **raw caller-supplied content** before entry-template wrapping (so `format: "entry"` and `format: "raw"` share one budget). Enforced by the tool layer, not the backend — see 0.5.1 fix notes. Zero is unused as a sentinel: `applyWriteDefaults` rewrites it to the 2000-byte safe default when `writable: true`, so an explicit `max_entry_size: 0` does not mean "unlimited". Set a very large number if you truly want no effective cap. |
| `file_locking`   | string              | no       | `flock`  | Phase 4. `flock` takes a POSIX advisory lock (LOCK_EX) around each write, cooperating with other flock-aware writers on the same machine. `none` skips locking and relies on pagefault's per-writer mutex only — single-writer deployments only. |

**Writable backends are read-only by default.** `writable: false` is the
zero value and the safe default. A read-only filesystem backend exposes
exactly the Phase-1 surface (`pf_peek`, `pf_scan`, `pf_load`) and nothing
else — `pf_poke` attempts against it terminate with
`403 access_violation`.

**Sandbox and writes.** When `sandbox: true` is also set (which you
want), the write path resolver walks up the parent chain of the target
URI to find the first existing component, resolves its symlinks, and
refuses the write if the resolved path escapes `root`. This protects
against a `root/notes → /etc` symlink being used to write
`memory://notes/leak.md` into `/etc/leak.md`.

**Example — a typical personal memory write config:**

```yaml
- name: fs
  type: filesystem
  root: "/home/jet/.openclaw/workspace"
  include: ["memory/**/*.md", "MEMORY.md"]
  uri_scheme: "memory"
  sandbox: true
  writable: true
  write_paths:
    - "memory://memory/20*.md"    # daily notes
    - "memory://memory/todos.md"  # todo list
    - "memory://MEMORY.md"        # long-term memory
  write_mode: "append"
  max_entry_size: 2000
  file_locking: "flock"
```

### `subprocess` backend

Runs an external command to answer `Search` requests. Canonical use:
ripgrep. `Read` is not supported — point a filesystem backend at the
same roots if you need content too.

```yaml
- name: rg
  type: subprocess
  command: "rg --json -i -n --max-count 20 {query} {roots}"
  roots:
    - "/home/jet/.openclaw/workspace/memory"
    - "/home/jet/.openclaw/self-improving"
  timeout: 10
  parse: "ripgrep_json"
```

| Field     | Type     | Required | Default    | Notes |
|-----------|----------|----------|------------|-------|
| `name`    | string   | yes      | —          | Backend name. |
| `type`    | string   | yes      | —          | Must be `subprocess`. |
| `command` | string   | yes      | —          | Tokenized command template. Each token is a separate argv element — **no shell interpretation**, so `{query}` cannot break out of its slot. Placeholders: `{query}`, `{roots}`. A bare `{roots}` token is spliced in as multiple argv elements. |
| `roots`   | string[] | no       | —          | Directories passed into `{roots}`. |
| `timeout` | int      | no       | `10`       | Seconds before the command is killed. |
| `parse`   | string   | no       | `plain`    | Stdout parser: `ripgrep_json` (ripgrep `--json`), `grep` (`path:lineno:content`), or `plain` (one snippet per line, no URI). |

The backend treats `exit 1` as "no matches" (matches grep/ripgrep
conventions) and returns an empty result. Any other non-zero exit, or a
timeout, surfaces as `ErrBackendUnavailable` (HTTP 502).

### `http` backend

Generic HTTP search backend. Issues a single HTTP request per
`Search` call, extracts a result array from the JSON response, and
converts each entry into a `SearchResult`.

```yaml
- name: lcm
  type: http
  base_url: "http://127.0.0.1:6443"
  auth:
    mode: "bearer"
    token: "${OPENCLAW_GATEWAY_TOKEN}"
  search:
    method: "POST"
    path: "/api/lcm/search"
    body_template: '{"query": "{query}", "limit": {limit}}'
    response_path: "results"
  timeout: 15
```

| Field                    | Type   | Required | Default | Notes |
|--------------------------|--------|----------|---------|-------|
| `name`                   | string | yes      | —       | Backend name. |
| `type`                   | string | yes      | —       | Must be `http`. |
| `base_url`               | string | yes      | —       | Root URL. Trailing slash is stripped. |
| `auth.mode`              | string | no       | none    | `bearer` to send an `Authorization: Bearer …` header. |
| `auth.token`             | string | required for bearer | —  | Bearer token. `${ENV}` substitution happens at config-load time. |
| `search.method`          | string | no       | `POST`  | HTTP method. |
| `search.path`            | string | yes      | —       | Appended to `base_url`. |
| `search.headers`         | map    | no       | —       | Extra request headers. |
| `search.body_template`   | string | no       | —       | Request body with `{query}` and `{limit}` placeholders. `{query}` is JSON-escaped. If empty, no body is sent. |
| `search.response_path`   | string | no       | —       | Dotted path to the array in the response JSON (e.g. `results` or `data.items`). `$.`-prefix is tolerated. Empty means "response is the array itself". |
| `timeout`                | int    | no       | `15`    | Seconds before the request is cancelled. |

Each array element in the response is coerced into a `SearchResult`.
Recognised keys (all optional): `uri`, `snippet`, `score`, `metadata`.
Unknown keys are ignored.

`Read` is not supported on generic HTTP backends.

### `subagent-cli` backend

Spawns an external agent process for `pf_fault`. The subagent is
responsible for doing its own retrieval; pagefault just runs the
command and waits for stdout.

```yaml
- name: openclaw
  type: subagent-cli
  command: "openclaw agent run --agent {agent_id} --task {task} --timeout {timeout}"
  timeout: 300
  agents:
    - id: wocha
      description: "Dev agent with Feishu, LCM, workspace, and coding tools"
    - id: main
      description: "Primary personal agent with full tool access"
```

| Field        | Type     | Required | Default | Notes |
|--------------|----------|----------|---------|-------|
| `name`       | string   | yes      | —       | Backend name. |
| `type`       | string   | yes      | —       | Must be `subagent-cli`. |
| `command`    | string   | yes      | —       | Tokenized command template. Placeholders: `{agent_id}`, `{task}`, `{timeout}`. Same non-shell tokenization as `subprocess`. |
| `timeout`    | int      | no       | `300`   | Default seconds before the child is killed. Overridden per call by `pf_fault.timeout_seconds`. |
| `agents`     | [object] | yes      | —       | At least one. Each has an `id` (required) and `description` (optional). The first entry is the default when `pf_fault.agent` is empty. |

On deadline, the process is killed; any stdout captured so far is
returned as `partial_result` with `timed_out: true`.

### `subagent-http` backend

Same role as `subagent-cli` but spawns the agent via HTTP. Useful when
agents live behind a gateway.

```yaml
- name: openclaw-http
  type: subagent-http
  base_url: "https://localhost:6443/api"
  auth:
    mode: "bearer"
    token: "${OPENCLAW_GATEWAY_TOKEN}"
  spawn:
    method: "POST"
    path: "/agents/{agent_id}/run"
    body_template: '{"task": "{task}", "timeout": {timeout}}'
    response_path: "result"
  timeout: 300
  agents:
    - id: wocha
      description: "Dev agent with Feishu, LCM, workspace, and coding tools"
```

| Field                  | Type     | Required | Default | Notes |
|------------------------|----------|----------|---------|-------|
| `name`                 | string   | yes      | —       | Backend name. |
| `type`                 | string   | yes      | —       | Must be `subagent-http`. |
| `base_url`             | string   | yes      | —       | Root URL. |
| `auth.mode`            | string   | no       | none    | `bearer` supported. |
| `auth.token`           | string   | required for bearer | — | Bearer token (supports `${ENV}`). |
| `spawn.method`         | string   | no       | `POST`  | HTTP method. |
| `spawn.path`           | string   | yes      | —       | Appended to `base_url`. `{agent_id}` in the path is substituted. |
| `spawn.headers`        | map      | no       | —       | Extra request headers. |
| `spawn.body_template`  | string   | no       | —       | Body template with `{agent_id}`, `{task}`, `{timeout}`. `{task}` is JSON-escaped. |
| `spawn.response_path`  | string   | no       | —       | Dotted path to the agent's response string. Non-string leaves are re-encoded as JSON. Empty means "the whole response body is the answer". |
| `timeout`              | int      | no       | `300`   | Default seconds. Overridden per call. |
| `agents`               | [object] | yes      | —       | At least one. Each has `id` and `description`. |

---

## `contexts`

Pre-composed bundles of backend resources that clients can request by name.

```yaml
contexts:
  - name: user-profile
    description: "User profile and preferences"
    sources:
      - backend: fs
        uri: "memory://USER.md"
      - backend: fs
        uri: "memory://IDENTITY.md"
    format: "markdown"
    max_size: 8000
```

| Field          | Type     | Required | Default    | Notes |
|----------------|----------|----------|------------|-------|
| `name`         | string   | yes      | —          | Region name (unique). |
| `description`  | string   | no       | —          | Shown by `pf_maps`. |
| `sources`      | [object] | yes      | —          | At least one source required. |
| `sources[].backend` | string | yes    | —          | Backend name from the `backends` section. |
| `sources[].uri`     | string | yes    | —          | URI to load. |
| `sources[].params`  | object | no     | —          | Reserved for future dynamic-source features; currently accepted but ignored. |
| `format`       | string   | no       | `markdown` | Output format: `markdown`, `markdown-with-metadata`, or `json`. Clients can override per request via `pf_load.format`. |
| `max_size`     | int      | no       | `16000`    | Max characters before truncation. Truncation is UTF-8-safe (rune-aligned). |

When a source cannot be read (missing file, filter block), the source is
dropped from the concatenated output but recorded in the `pf_load` response
under `skipped_sources` (with a reason) and logged at `WARN` level. The
request as a whole is not aborted.

---

## `tools`

Enable or disable individual tools. All tools default to enabled.

```yaml
tools:
  pf_maps:  true    # list_contexts: memory regions / contexts
  pf_load:  true    # get_context: load a region
  pf_scan:  true    # search across backends
  pf_peek:  true    # read a resource by URI
  pf_fault: true    # deep_retrieve — Phase 2, requires a SubagentBackend
  pf_ps:    true    # list_agents  — Phase 2, lists configured subagents
  pf_poke:  true    # write        — Phase 4 (shipped 0.5.0); needs a
                    # writable backend for mode:direct or a subagent
                    # backend for mode:agent
```

`pf_fault` and `pf_ps` are only useful when at least one
`subagent-cli` or `subagent-http` backend is configured. They can still
be enabled without one — `pf_ps` returns an empty agent list and
`pf_fault` returns `404 agent not found`.

---

## `filters`

Optional filter pipeline. Can be disabled entirely with `enabled: false`.

```yaml
filters:
  enabled: true
  path:
    allow: []                             # empty = allow all URIs for reads
    deny:
      - "memory://memory/intimate.md"
      - "self-improving://**/corrections.md"
    # Phase 4 — write-only allowlist/denylist. When at least one of
    # these is set, writes are checked exclusively against this pair
    # (not the read allow/deny above). When both are empty, writes
    # fall through to the read allow/deny pair.
    write_allow:
      - "memory://memory/20*.md"
      - "memory://memory/todos.md"
    write_deny: []
  tags:
    allow: []
    deny: ["intimate"]
  redaction:
    enabled: true
    rules:
      - pattern: '(?i)api[_-]?key\s*[:=]\s*\S+'
        replacement: "[REDACTED]"
      - pattern: 'pf_[A-Za-z0-9]+'
        replacement: "[TOKEN]"
```

| Field              | Type                     | Notes |
|--------------------|--------------------------|-------|
| `enabled`          | bool                     | Master switch — false turns everything into a pass-through. |
| `path.allow`       | string[] (glob)          | URI read allowlist. Empty = allow all. |
| `path.deny`        | string[] (glob)          | URI read denylist. Deny beats allow. |
| `path.write_allow` | string[] (glob)          | Phase 4. URI write allowlist. When set, writes are checked against this list instead of `allow`. |
| `path.write_deny`  | string[] (glob)          | Phase 4. URI write denylist. |
| `tags.allow`       | string[]                 | Resources without any matching tag are hidden. |
| `tags.deny`        | string[]                 | Resources with any matching tag are hidden. |
| `redaction.enabled`| bool                     | Compiles `rules` into a content filter. Unused rules (with `enabled: false`) are ignored. |
| `redaction.rules`  | []object                 | Each rule has `pattern` (Go regexp) and `replacement` (supports `$1`/`$2` capture groups). Invalid patterns fail fast at server start. |

**Read broadly, write narrowly.** The canonical reason to set
`path.write_allow` (or `write_deny`) instead of reusing the read
lists is so an agent can freely read any memory URI but only write to
a handful of specific files — e.g., today's daily note and the TODO
list. When either write list is non-empty, the read `allow`/`deny`
pair is **ignored** for writes.

This server-wide write filter stacks on top of the backend's own
`write_paths` allowlist — both must allow a URI for a write to
proceed. See the `filesystem` backend section above for the
per-backend write config.

### Pattern syntax

Doublestar globs (`**` for recursive match) for path patterns, plain strings
for tags, and Go `regexp` syntax for redaction rules (`(?i)` inline flag,
`\d`, `$1` capture groups, etc.).

### Order of operations

1. **URI check** (pre-fetch, reads): `path.allow` + `path.deny` — blocked URIs never
   hit the backend.
2. **Write URI check** (pre-write, pf_poke only): `path.write_allow` +
   `path.write_deny` if set, otherwise `path.allow` + `path.deny`.
   Runs before the backend's per-backend `write_paths` check.
3. **Tag check** (post-fetch): `tags.allow` + `tags.deny` — applied to
   resource metadata tags.
4. **Content transform**: `redaction.rules` — every rule is applied in
   declaration order. The transformed content is what the caller receives
   (no unredacted copy hits the wire).

---

## `audit`

```yaml
audit:
  enabled: true
  mode: "jsonl"             # "jsonl" | "stdout" | "off"
  log_path: "/var/log/pagefault/audit.jsonl"
  include_content: false    # reserved for future use
```

| Field             | Type   | Notes |
|-------------------|--------|-------|
| `enabled`         | bool   | Master switch. Implied mode is `off` when false. |
| `mode`            | enum   | `jsonl`, `stdout`, or `off`. If empty, inferred from `enabled` + `log_path`. |
| `log_path`        | path   | Required when `mode: jsonl`. Parent directory must exist. |
| `include_content` | bool   | Reserved — not honored in Phase 1. |

Each audit entry is a JSON line with fields:
`timestamp`, `caller_id`, `caller_label`, `tool`, `args` (sanitized),
`duration_ms`, `result_size`, `error`.

Sensitive fields in `args` (anything containing `token`, `password`, `secret`,
`api_key`, `authorization`) are automatically replaced with `[REDACTED]`.

---

## Environment variable substitution

`${VAR}` references in the YAML are expanded using `os.ExpandEnv` before
parsing. Example:

```yaml
auth:
  mode: "bearer"
  bearer:
    tokens_file: "${PAGEFAULT_TOKENS_FILE}"
```

Undefined variables expand to the empty string, which will usually surface as
a validation error for required fields.
