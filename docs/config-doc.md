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
  public_url: ""        # optional, used by OpenAPI spec (Phase 3)
```

| Field        | Type   | Default       | Notes |
|--------------|--------|---------------|-------|
| `host`       | string | `127.0.0.1`   | Bind address. |
| `port`       | int    | `8444`        | Listen port. |
| `public_url` | string | empty         | Reserved for Phase 3 OpenAPI spec generation. |

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

| Field        | Type               | Required | Default | Notes |
|--------------|--------------------|----------|---------|-------|
| `name`       | string             | yes      | —       | Unique backend identifier. |
| `type`       | string             | yes      | —       | Must be `filesystem`. |
| `root`       | path               | yes      | —       | Directory to serve. Resolved to an absolute path at startup. |
| `include`    | string[]           | no       | all     | Doublestar globs; files must match at least one to be visible. |
| `exclude`    | string[]           | no       | —       | Globs; files matching any are hidden. |
| `uri_scheme` | string             | yes      | —       | URI scheme for this backend (e.g. `memory`). |
| `auto_tag`   | map[string][]string| no       | —       | Path glob → tag list. Tags become resource metadata and are visible to the tag filter. |
| `sandbox`    | bool               | no       | `false` | If true, reject any file whose resolved path (after symlink resolution) escapes `root`. |

**Backends do not expose writes in Phase 1.** Write support is Phase 4.

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
| `sources[].params`  | object | no     | —          | Reserved for dynamic sources (Phase 2). |
| `format`       | string   | no       | `markdown` | Output format: `markdown` or `json`. |
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
  pf_poke:  false   # write        — Phase 4, not yet implemented
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
    allow: []                             # empty = allow all URIs
    deny:
      - "memory://memory/intimate.md"
      - "self-improving://**/corrections.md"
  tags:
    allow: []
    deny: ["intimate"]
  redaction:                              # Phase 3 only
    enabled: false
    rules:
      - pattern: '(?i)api[_-]?key\s*[:=]\s*\S+'
        replacement: "[REDACTED]"
```

| Field              | Type                     | Notes |
|--------------------|--------------------------|-------|
| `enabled`          | bool                     | Master switch — false turns everything into a pass-through. |
| `path.allow`       | string[] (glob)          | URI allowlist. Empty = allow all. |
| `path.deny`        | string[] (glob)          | URI denylist. Deny beats allow. |
| `tags.allow`       | string[]                 | Resources without any matching tag are hidden. |
| `tags.deny`        | string[]                 | Resources with any matching tag are hidden. |
| `redaction.*`      | (Phase 3)                | Regex redaction on content. Not implemented in Phase 1. |

### Pattern syntax

Doublestar globs (`**` for recursive match) for path patterns, plain strings
for tags.

### Order of operations

1. **URI check** (pre-fetch): `path.allow` + `path.deny` — blocked URIs never
   hit the backend.
2. **Tag check** (post-fetch): `tags.allow` + `tags.deny` — applied to
   resource metadata tags.
3. **Content transform** (Phase 3): `redaction.rules`.

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
