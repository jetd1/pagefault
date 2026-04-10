# pagefault — Security Model

This document describes pagefault's threat model, trust boundaries, and the
mechanisms the server uses to keep personal data from leaking to untrusted
clients. It tracks `plan.md` §12 and the code in `internal/auth`,
`internal/filter`, `internal/backend`, and `internal/audit`. If this doc
drifts from any of those, update it.

## Trust model

pagefault sits between **one trusted operator** (you) and **N AI clients**.
The operator writes the config, owns the filesystem, and holds the bearer
tokens; clients only ever see what the config and filters allow them to see.

```
┌────────────┐    token      ┌───────────┐    filter+audit   ┌──────────┐
│ AI client  │───────────▶───│ pagefault │──────────▶────────│ backend  │
│  (Claude,  │    HTTP/      │  server   │                   │ (files…) │
│  ChatGPT…) │    MCP        └───────────┘                   └──────────┘
└────────────┘
     │   (untrusted)                (trusted operator zone)
```

- **Inside the trust boundary:** the operator's machine, the pagefault
  binary, the YAML config, backend data sources, the audit log.
- **Outside the trust boundary:** every MCP/REST client, the LLM providers
  they forward data to, and anything reachable over the network.

pagefault does **not** attempt to protect data from the LLM provider once
the content has left the server — that is the same trust decision you made
when you started using Claude/ChatGPT.

## Auth

Configured under `auth:` in the YAML config. Three modes are supported:

| Mode              | When to use                               | How it checks callers |
|-------------------|-------------------------------------------|-----------------------|
| `bearer`          | Normal remote access                      | `Authorization: Bearer <tok>` matched against `tokens.jsonl` |
| `trusted_header`  | Behind a reverse proxy that authenticates | Reads a header (e.g. `X-Pagefault-User`) and enforces `trusted_proxies` |
| `none`            | Local dev / loopback only                 | Every request is treated as `anonymous` |

Tokens are managed by the `pagefault token` subcommand — `create`, `ls`,
`revoke` — and stored in `tokens.jsonl`, one record per line. Properties:

- Tokens are 32 bytes of `crypto/rand`, base64-URL encoded, prefixed `pf_`.
- Each record has an ID + label; revoke by ID.
- The full token is printed **once** at create time and never again.
- `pagefault token ls` masks token values to `prefix…suffix`.
- The tokens file is written atomically (temp file + rename) and should be
  mode `0600`.

Auth middleware is applied to every authenticated route, including the MCP
transport at `/mcp` and `/mcp/*`. Callers that fail auth receive `401` with
a `WWW-Authenticate` header; trusted-header auth returns `403` if the source
IP is not in `trusted_proxies`.

> **Note on MCP sessions.** mcp-go's streamable-http transport uses long
> lived session IDs. The chi middleware re-runs on every HTTP request
> (including the post-initialize tool calls), so bearer tokens are validated
> on every hop — the session ID alone is not a substitute for auth. If you
> find a client that opens a session once and never re-sends its
> `Authorization` header, that is a client bug and you should report it.
> Audit log entries will show the caller as `anonymous` if the token is
> missing, which is a useful signal.

## Sandbox & path traversal

The Phase-1 filesystem backend enforces a sandbox on every read:

1. The configured `root` is resolved to an absolute path via
   `filepath.EvalSymlinks`. A root that points outside the expected
   directory is rejected at server start.
2. Every URI is translated to a filesystem path, resolved via
   `EvalSymlinks`, and checked with `filepath.Rel` against the sandbox
   root. Paths that `..` their way out are rejected as
   `ErrAccessViolation`.
3. The `include` glob list gates which files inside `root` are visible at
   all; `exclude` removes matches from the set.
4. URIs use a backend-specific scheme (e.g. `memory://`) and the
   dispatcher routes by scheme, so no client can ask the filesystem
   backend for a file outside its namespace by spelling a `file://` URI.

Symlinks that point outside `root` are rejected. Symlinks inside `root`
are followed.

## Filters

Configured under `filters:` in the YAML config. All filters are
**optional** — if `filters.enabled: false`, the dispatcher uses a
pass-through filter that accepts everything.

The filter pipeline runs three checks per tool call:

1. **AllowURI (pre-fetch).** Cheap path/URI check. Runs before any backend
   I/O so denied URIs never hit the disk.
2. **AllowTags (post-fetch).** Tag-based check. Runs after the backend
   returns a resource (because tags come from resource metadata, not from
   the URI).
3. **FilterContent.** Content-level transforms (e.g. redaction regexes).
   Runs last, so it sees the final content the client will receive.

The filter types shipped in Phase 1:

| Filter       | Purpose                                       |
|--------------|-----------------------------------------------|
| `PathFilter` | Allow/deny by glob (`docs/**/*.md`, `**/secrets/**`) |
| `TagFilter`  | Allow/deny by metadata tags                   |
| `Redaction`  | Regex-based content masking                   |

Filters compose with **AND** semantics: every configured filter must allow
a URI for the dispatcher to serve it.

### Skipped sources

`pf_load` concatenates multiple sources into one bundle. If the filter
blocks a URI, or the backend read fails, the source is **skipped rather
than failing the whole request**. The dispatcher:

- Logs each skip at `WARN` level via `log/slog`, including the URI and a
  reason (`blocked by uri filter`, `blocked by tag filter`, `read error:
  <msg>`).
- Returns a `skipped_sources` list alongside the content so the client can
  see which URIs were omitted and why.

This prevents the "silent hole" failure mode where a filter eats half of a
context and the user has no idea why.

## Audit log

Every dispatcher call produces an audit entry:

```json
{"ts":"2026-04-10T20:42:00Z","caller_id":"laptop","caller_label":"MacBook",
 "tool":"pf_peek","args":{"uri":"memory://notes/daily.md","from_line":0,"to_line":0},
 "duration_ms":3,"result_size":412,"error":null}
```

Properties:

- Written to `audit.jsonl` by default, one JSON object per line.
- Writes are mutex-serialised inside the process; use a separate file per
  instance if you run multiple copies.
- `SanitizeArgs` redacts any arg whose key matches `token`, `password`,
  `secret`, `api_key`, or `authorization` to `[REDACTED]` before the entry
  is written.
- Bearer tokens themselves never appear in the audit log; only the token
  record's `id` and `label` do.
- Stdout and nop loggers are available for dev / test.

If the audit log disk fills up, writes will error and the tool call will
return a `500`. This is intentional — the alternative is losing audit
coverage silently.

## Write safety (Phase 4+)

Phase 1 is **read-only**. There is no `pf_poke` tool, no writable backend
config, and no `write_paths` allowlist. When Phase 4 lands, the following
protections apply:

- Backends default to `writable: false`. Turning on writes is explicit.
- Write allowlist (`write_paths`) is separate from the read allowlist.
- `write_mode: "append"` by default; `"any"` must be explicitly configured.
- `format: "entry"` auto-wraps the content with a timestamp header,
  preventing raw injection into frontmatter.
- `max_entry_size` caps individual write payloads.
- `flock` serialises concurrent writers.
- Agent writeback (`mode: "agent"`) delegates writes to a subagent that
  runs under its own safety envelope.
- Every write is audit-logged with before/after metadata.

Do not enable writes without reading this section.

## Threats and mitigations

| Threat                                    | Mitigation                                                                                          |
|-------------------------------------------|-----------------------------------------------------------------------------------------------------|
| Unauthorized client reads                 | Bearer tokens or trusted-header auth on every request; `none` only for loopback dev.                |
| Path traversal                            | `EvalSymlinks` + `isUnder` sandbox on every filesystem read; `include`/`exclude` glob gate.          |
| Sensitive data exposure to an allowed client | `filters.path.deny`, `filters.tags.deny`, and `filters.redaction` keep specified content off the wire. |
| Data leaving the perimeter                | Acknowledged: content served to a client enters that client's model provider. Filters are the lever. |
| Token theft                               | Per-device tokens, revocable by ID; audit log shows exactly what each token touched.                |
| Backend timeout / hang                    | `context.Context` is threaded through every call; clients receive `502` on backend failure.         |
| Log poisoning via tool args               | `SanitizeArgs` strips known-sensitive keys before writing to the audit log.                         |
| Partial context with silent data loss     | `skipped_sources` surfaced in `pf_load` output; skips logged at `WARN`.                             |

## Known limitations

- **Naive scan.** Phase 1 `pf_scan` is a case-insensitive substring scan
  across every included file. It is fine for small trees (tens to a few
  hundred files) but does not scale. Phase 2 will add indexed / semantic
  search via subprocess or subagent backends.
- **No rate limiting.** Phase 1 trusts the operator to run the server
  behind a reverse proxy or on loopback. A single malicious client can
  stall the server with large search queries.
- **No per-tool auth.** Any authenticated caller can invoke any enabled
  tool. Per-token tool ACLs are a future addition.
- **No Phase-1 TLS.** Terminate TLS at a reverse proxy (Caddy, nginx,
  Cloudflare) — pagefault does not ship its own cert handling.
- **Audit log is append-only but not tamper-evident.** A compromised host
  can rewrite the log; for tamper-evidence, ship entries off-host.

## Checklist for a safe deployment

- [ ] Config is loaded from a file owned by the operator, mode `0600`.
- [ ] `auth.mode` is `bearer` or `trusted_header` (never `none` on a
      reachable address).
- [ ] `tokens.jsonl` is mode `0600` and not committed to git.
- [ ] Filesystem backends have `sandbox: true` and a narrow `root`.
- [ ] `include` globs scope the visible tree; `exclude` covers the
      obvious secrets (`**/.env`, `**/credentials*`, `**/*token*`).
- [ ] `filters.enabled: true` unless you have a specific reason.
- [ ] Audit log path is writable and monitored for disk space.
- [ ] The service is bound to loopback or to a reverse proxy that adds
      TLS and rate limiting.
- [ ] Tokens are rotated after any device loss.
