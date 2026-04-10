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

A minimal working config is in `configs/minimal.yaml`.

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
`type`. Phase 1 supports only `type: filesystem`.

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

### Planned types (future phases)

| Type             | Phase | Notes |
|------------------|-------|-------|
| `subprocess`     | 2     | Shell command template (e.g., `rg --json ...`). |
| `http`           | 2     | Generic HTTP API backend. |
| `subagent-cli`   | 2     | Spawn an agent via a CLI command. |
| `subagent-http`  | 2     | Spawn an agent via an HTTP API. |

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
  pf_fault: false   # deep_retrieve — Phase 2, ignored in Phase 1
  pf_ps:    false   # list_agents  — Phase 2, ignored in Phase 1
  pf_poke:  false   # write        — Phase 4, ignored in Phase 1
```

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
