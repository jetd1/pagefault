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

Configured under `auth:` in the YAML config. Four modes are supported:

| Mode              | When to use                               | How it checks callers |
|-------------------|-------------------------------------------|-----------------------|
| `bearer`          | Normal remote access                      | `Authorization: Bearer <tok>` matched against `tokens.jsonl` |
| `trusted_header`  | Behind a reverse proxy that authenticates | Reads a header (e.g. `X-Pagefault-User`) and enforces `trusted_proxies` |
| `oauth2`          | Claude Desktop native SSE (0.7.0+)        | RFC 6749 §4.4 client_credentials grant against `oauth-clients.jsonl`; can run compound with `bearer` as a fallback |
| `none`            | Local dev / loopback only                 | Every request is treated as `anonymous` |

Tokens are managed by the `pagefault token` subcommand — `create`, `ls`,
`revoke` — and stored in `tokens.jsonl`, one record per line. Properties:

- Tokens are 32 bytes of `crypto/rand`, base64-URL encoded, prefixed `pf_`.
- Each record has an ID + label; revoke by ID.
- The full token is printed **once** at create time and never again.
- `pagefault token ls` masks token values to `prefix…suffix`.
- The tokens file is written atomically (temp file + rename) and should be
  mode `0600`.

### OAuth2 client_credentials (0.7.0+)

`mode: "oauth2"` runs an RFC 6749 §4.4 client_credentials grant
against an operator-managed client registry (`oauth-clients.jsonl`,
managed by `pagefault oauth-client create / ls / revoke`). The
provider exists primarily to make pagefault reachable from Claude
Desktop's native SSE MCP configuration, which only exposes Client
ID / Client Secret credential fields.

Security-relevant properties of the implementation:

- **Client secrets are bcrypt-hashed** (`golang.org/x/crypto/bcrypt`
  at `DefaultCost`). The plaintext secret is printed exactly once
  at `oauth-client create` time and stored only as the hash. A
  dumped `oauth-clients.jsonl` therefore reveals client IDs +
  labels + scopes but not the secrets themselves.
- **Access tokens are 32 bytes of `crypto/rand`**, base64-URL
  encoded, prefixed `pf_at_` (distinct from the long-lived `pf_`
  prefix used by bearer tokens). Tokens live in an in-memory map
  only — they do not persist across server restarts, so a restart
  is an authoritative way to invalidate every outstanding token.
- **Token TTL is enforced on lookup.** `Authenticate` compares the
  stored `ExpiresAt` against `time.Now()` and both rejects the
  request *and* evicts the entry when expired. A follow-up write
  lock re-checks before deleting to handle the rare race where
  another request refreshed the same key. Opportunistic sweeps
  also run on every `IssueToken` call, bounding memory even if
  no expired lookups happen.
- **The two discovery endpoints are intentionally public.** RFC
  9728 and RFC 8414 require them to be reachable without
  credentials because clients bootstrap through them *before*
  they have a token. They advertise only the issuer, token
  endpoint, supported grants, and supported auth methods — no
  scope-sensitive or client-sensitive information.
- **`POST /oauth/token` is outside the bearer middleware.** It
  authenticates via the client credentials carried in either the
  Authorization Basic header (RFC 6749 §2.3.1) or the form body
  (§2.3.2). Credential failure returns RFC 6749 §5.2 error
  envelopes: 401 + `WWW-Authenticate: Basic` when the client used
  Basic, 400 when it used form body. The error code is always
  `invalid_client`; we do not leak whether the failure was
  "unknown id" vs "wrong secret" to avoid enabling client-id
  enumeration.
- **Compound mode** (`oauth2` + populated `bearer.tokens_file`)
  runs the OAuth2 store first and falls back to the bearer store
  on no match. The fallback re-uses the existing `BearerTokenAuth`
  implementation, so audit entries and caller metadata are
  identical regardless of which store matched. Operators can
  audit by checking `caller.metadata.auth` — it is `"oauth2"`
  for OAuth2-issued tokens and absent for legacy bearer tokens.
- **Scope enforcement is coarse in 0.7.0.** Scopes are attached
  to issued tokens (as the intersection of the client's allowed
  set and the caller-requested set) but no tool currently
  branches on them — any authenticated caller can invoke any
  enabled tool. Per-scope tool ACLs are a future addition;
  until then, scope narrowing is primarily a forward-compatibility
  knob plus an audit trail hint.
- **Revocation semantics.** `pagefault oauth-client revoke <id>`
  removes the client record so no new tokens can be issued for
  that client, but **access tokens that have already been issued
  remain valid until their TTL expires or the server restarts**.
  The CLI prints this explicitly after every revoke. For
  immediate invalidation, restart pagefault.
- **No refresh tokens, no PKCE, no dynamic client registration**
  in the 0.7.0 implementation. Claude Desktop re-exchanges
  client_id/client_secret for a new access token automatically
  when its cached one expires, so refresh tokens are unnecessary
  for the target workload. PKCE protects against authorization
  code interception in the authorization_code flow, which we do
  not implement. Dynamic client registration would open a
  zero-auth endpoint that creates privileged records on a
  single-operator server — operators can register clients via
  the CLI instead.

Auth middleware is applied to every authenticated route, including both
MCP transports: streamable-http at `/mcp` and `/mcp/*`, and legacy SSE
at `GET /sse` + `POST /message?sessionId=...`. Callers that fail auth
receive `401` with a `WWW-Authenticate` header; trusted-header auth
returns `403` if the source IP is not in `trusted_proxies`.

> **Note on MCP sessions.** mcp-go's streamable-http transport and its
> legacy SSE transport both use long-lived session IDs. The chi
> middleware re-runs on every HTTP request — the initial `GET /sse` open,
> every `POST /message?sessionId=…`, every `POST /mcp` tool call — so
> bearer tokens are validated on every hop and the session ID alone is
> not a substitute for auth. A well-behaved MCP client re-sends its
> `Authorization` header on every request; if you find one that opens
> a session once and never re-sends it, that is a client bug and you
> should report it. Audit log entries will show the caller as
> `anonymous` if the token is missing, which is a useful signal.

> **Claude Desktop caveat (as of 2026-04).** Claude Desktop's built-in
> SSE configuration UI only exposes **OAuth2 Client ID / Client
> Secret** fields for extra credentials — it does not expose a way to
> attach a plain `Authorization: Bearer pf_...` header to the SSE GET.
> For a bearer-auth deployment of pagefault, the practical paths are
> (a) use the `npx supergateway --streamableHttp` bridge to inject the
> bearer header via a local stdio adapter (the supergateway config in
> `README.md`), or (b) switch to `auth.mode: oauth2` (shipped in 0.7.0)
> and register a Claude Desktop OAuth2 client via
> `pagefault oauth-client create`. See the `Auth → OAuth2` section
> below for the full flow.

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

Filter types currently shipped:

| Filter       | Purpose                                              | Phase |
|--------------|------------------------------------------------------|-------|
| `PathFilter` | Allow/deny by glob (`docs/**/*.md`, `**/secrets/**`) | 1     |
| `TagFilter`  | Allow/deny by metadata tags                          | 1     |
| `Redaction`  | Regex-based content masking (Go `regexp` patterns, capture groups in replacement) | 3 |

Filters compose with **AND** semantics: every configured filter must allow
a URI for the dispatcher to serve it. Redaction rules run in the
`FilterContent` stage after path and tag checks, so the transformed
content is what the caller receives — the un-redacted copy never
leaves the dispatcher. Rules are compiled at server start, so an
invalid pattern fails fast rather than at request time.

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

## Write safety (Phase 4, shipped in 0.5.0)

pagefault exposes mutations through a single tool — `pf_poke` — and
every filesystem backend is **read-only by default**. Enabling writes
is a deliberate opt-in: an operator has to set `writable: true` on a
specific backend, and even then the tool is gated by two independent
allowlists plus size and mode caps.

**What "default is read-only" means in practice.** With a zero-config
filesystem backend (the shape `configs/minimal.yaml` ships), every
`pf_poke` call terminates with `403 access_violation` before any
file-system syscall is issued. Even if a bug in the filter layer let a
request through, the backend's `Writable()` check at the top of
`filesystem.go`'s `Write` method would reject it. The two layers are
independent by construction.

### Mode: direct (pagefault appends the content)

Five layers gate each direct-append call. A write must pass *all* of
them to reach the disk:

1. **Tool enable flag.** `tools.pf_poke: false` in config removes the
   endpoint entirely (no MCP registration, no REST route).
2. **Server-wide write filter.** `filters.path.write_allow` /
   `write_deny` (Phase 4 additions to `PathFilter`). When empty, the
   read allow/deny pair is used as a fallback; when either is set,
   writes are checked exclusively against the write pair — "read
   broadly, write narrowly" as a config pattern.
3. **Backend Writable() flag.** The filesystem backend type-asserts
   to `WritableBackend`; if `writable: false` (the zero value) the
   dispatcher rejects before calling into the backend.
4. **Per-backend write_paths.** A doublestar URI glob list. Empty
   means "every include-eligible URI", but the canonical config
   names exact files (`memory://memory/20*.md`, `memory://MEMORY.md`).
5. **Size + mode caps.** `max_entry_size` is measured on the raw
   caller content before entry-template wrapping, so `format: "raw"`
   and `format: "entry"` share the same budget. `write_mode: "any"`
   is a second-tier opt-in required for `format: "raw"` — the intent
   is that anyone typing `writable: true` gets entry-template-only
   semantics for free, and has to explicitly upgrade to raw bytes.

**Entry template.** `format: "entry"` (the default) wraps the content
as a leading-newline horizontal-rule timestamped block:

```
\n---\n## [HH:MM] via pagefault (<caller label>)\n\n<content>\n
```

Two consequences: (a) the block always starts on a fresh line even
when the existing file has no trailing newline (so injecting a
header that alters an earlier section isn't possible), and (b) the
timestamp+label give a short audit trail embedded in the document
itself. A caller who wants to bypass the template (e.g., to write a
preamble) has to configure `write_mode: "any"` on the backend *and*
pass `format: "raw"` on the request — the two-lock design is
deliberate.

**Sandbox for new files.** Filesystem `sandbox: true` already covers
existing files via `EvalSymlinks + isUnder`, but new-file writes
need extra work because the leaf doesn't exist yet at stat time.
`resolveWritePath` walks up the parent chain of the target, finds
the first existing component, resolves its symlinks, and refuses
the write if the resolved path escapes `root`. This means a
`notes → /etc` parent-directory symlink cannot be used to write
`memory://notes/leak.md` into `/etc/leak.md` on a cold cache.

> **Known limitation — TOCTOU race on `resolveWritePath`.** The
> symlink check and the subsequent `MkdirAll` + `OpenFile` are not
> atomic. An attacker who can mutate the filesystem under `root`
> between the check and the write can, in principle, swap a vetted
> parent directory for a symlink pointing outside `root` in the
> narrow window between the two. Exploiting this requires precise
> sub-millisecond timing AND local write access to the backend
> root — which is already the other side of pagefault's trust
> boundary in the single-operator deployment model (the operator
> owns the filesystem, per `docs/security.md` §Threat model).
> A proper fix would need `openat(O_NOFOLLOW)` on every path
> component; deferred until pagefault has a multi-tenant
> deployment story.

**Concurrency.** `flock(2)` on the open fd (LOCK_EX) serialises
writes with any other flock-aware writer on the host — editors,
the openclaw CLI, a parallel pagefault process. The per-writer
mutex holds while the file is open so pagefault itself is always
serialized even when flock is unavailable (`file_locking: "none"`,
which should only be used in single-writer deployments).

**Audit.** Every `pf_poke` call is logged with:

- Caller id + label (never the token).
- Tool name `pf_poke`.
- `uri` + `bytes` (the byte count of the content passed to the
  backend — for `format: "entry"` this is the wrapped body, ~40–60
  bytes larger than the raw request). The content body itself is
  never logged.
- Duration.
- `result_size` (bytes that actually hit disk — useful for spotting
  format=entry overhead; for the filesystem backend this equals
  `bytes`).
- Any sentinel error on failure.

> **Agent mode is audited under `tool: "pf_fault"`, not
> `"pf_poke"`.** Because `mode: "agent"` delegates to the
> `pf_fault` machinery (via `dispatcher.DeepRetrieve`), the audit
> entry it produces carries `tool: "pf_fault"` with the composed
> natural-language task in `args.query` (the task begins with
> `"A remote agent (<label>) wants to record ..."`). Operators
> auditing *all* writes must scan both `tool: "pf_poke"` entries
> (direct mode) and `tool: "pf_fault"` entries whose query begins
> with that prefix (agent mode). Emitting a duplicate `pf_poke`
> row for every agent-mode call was considered but rejected for
> 0.5.1 on the grounds that the underlying action really is a
> subagent spawn — the `pf_fault` log row is not misleading, it
> is just insufficient on its own. Revisit once we have
> structured subagent responses (see §Phase 5).

### Mode: agent (subagent decides where to write)

`mode: "agent"` hands the task to a subagent over the existing
`pf_fault` machinery. **Critical:** pagefault's `write_paths` and
`filters.path.write_*` do **not** apply to what the subagent
writes — they only gate pagefault's *request* (the content + target
hint passed to the spawn). The subagent has its own workspace
access, its own conventions, and its own guardrails.

Concretely, this means:

- A `subagent-cli` backend inherits pagefault's file-descriptor and
  environment posture. The agent process can write anywhere its
  credentials allow, subject only to whatever sandbox the operator
  wraps the command template in.
- A `subagent-http` backend sends the task to a remote endpoint;
  the remote service is entirely responsible for authorising,
  validating, and persisting the write. pagefault just forwards.
- Timeouts still apply (`timeout_seconds`, default 120). On timeout
  the tool flattens the failure into a `200 OK` envelope with
  `timed_out: true` and whatever partial stdout the agent produced,
  matching `pf_fault` semantics — so a caller cannot tell the
  difference between "agent finished cleanly" and "agent was killed
  mid-write" without reading the flag.
- The response envelope's `targets_written` array is **reserved**
  but **currently always absent**: pagefault forwards the
  subagent's textual reply via `result` and has no structured way
  to extract the list of URIs the subagent touched. Clients that
  want to know what was written must parse `result`, or wait for
  Phase 5's structured subagent envelope.

**Design rationale.** The plan.md §5.7 rationale is that direct mode
handles the 80% case (fixed format, known location) and agent mode
handles the 20% case (needs judgment about where to write, how to
format, whether to merge with existing content). The trust model
follows: direct mode is mechanical and sandboxed; agent mode is
smart and trusted.

Do not enable agent mode writes in environments where you would not
also enable direct subagent invocation via `pf_fault`. The threat
surface is identical.

## Subagent safety (Phase 2+)

`pf_fault` delegates a natural-language query to an external subagent
process (via `subagent-cli` or `subagent-http`). **The subagent runs
outside pagefault's sandbox** — the filter pipeline applies only to
the query and agent id, not to what the agent reads, writes, or sends
over the network. In effect, configuring a subagent backend is equivalent
to granting `pf_fault` clients the ability to invoke that agent with
arbitrary natural-language input.

Guardrails pagefault *can* enforce:

- **Timeouts.** Every call has a deadline (`pf_fault.timeout_seconds`,
  or the backend's `timeout` default). `exec.CommandContext` kills the
  child process on expiry; HTTP subagents cancel the request context.
- **Argv isolation.** `subagent-cli` tokenizes the command template
  into argv once at startup and does placeholder substitution per call
  — there is **no shell interpretation**, so a caller-supplied `{task}`
  cannot inject shell metacharacters.
- **JSON escaping.** `subagent-http` JSON-escapes `{task}` before
  substituting it into `body_template`, so a task containing quotes
  cannot break the request body.
- **Audit.** Every `pf_fault` call is logged with query, agent, backend,
  elapsed seconds, and any error — even timeouts (they surface in the
  structured response, not as errors).
- **Agent allowlist.** Only agents listed in `agents:` can be spawned.
  Unknown ids fail fast with `404 agent not found`.

Guardrails pagefault *cannot* enforce (operator responsibility):

- **What the agent reads or writes.** If the agent has filesystem
  access, it can read or modify files that pagefault's filter pipeline
  would otherwise block. Choose an agent whose own sandbox matches the
  trust level of `pf_fault` callers.
- **Outbound network calls.** A CLI agent inherits pagefault's
  network posture; an HTTP agent is entirely remote.
- **Resource exhaustion.** A caller who can spawn subagents can drive
  load on the subagent's runtime. Rate-limit `pf_fault` at a proxy if
  that matters.

> **Subagent prompt templates are a behaviour lever, not a security
> lever.** The `retrieve_prompt_template` / `write_prompt_template`
> wrapping (see `docs/config-doc.md` → "Subagent prompt templates")
> exists to frame a fresh agent as a memory retriever / placer so it
> does not fall back to generic Q&A mode — but a malicious or
> compromised subagent is free to ignore the framing. Do not treat
> the template as a sandbox, an input validator, or a jailbreak
> mitigation. Trust flows the same way it would without the
> template: `pf_fault` callers are still *users of the configured
> subagent* with whatever privileges that agent holds.

Bottom line: treat `pf_fault` callers as *users of the configured
subagent*, not as sandboxed memory clients. If you wouldn't let them
run the agent directly, don't give them pagefault access either.

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
| Shell injection via subagent task         | `subagent-cli` uses argv-per-token, no shell; `subagent-http` JSON-escapes the task before substitution. |
| Subagent hang / runaway                   | Per-call timeout enforced by `exec.CommandContext` (CLI) or request context (HTTP); partial stdout captured. |
| Unbounded access via subagent             | Acknowledged: subagents run outside pagefault's sandbox. Treat `pf_fault` callers as users of the configured agent. |
| Per-caller request floods                 | `server.rate_limit` enforces a per-caller token bucket keyed on `caller.id`; over-budget requests get 429 + `Retry-After`. |
| Browser cross-origin abuse                | `server.cors` is opt-in with an explicit origin allowlist; disabled by default. |
| Unauthorized writes (`pf_poke` direct)    | Backends default to `writable: false`; a write must pass the tool enable flag, the server-wide write filter, the backend `Writable()` flag, `write_paths`, and the `max_entry_size` cap — five independent gates. |
| Write payload dumping                     | `max_entry_size` (default 2000 bytes, measured on raw caller content) caps single-call payloads; oversized writes return 413 `content_too_large`. |
| Raw-byte injection into file headers      | `format: "entry"` wraps content with a leading newline + horizontal rule + timestamped header; `format: "raw"` is a second-tier opt-in that requires `write_mode: "any"` on the target backend. |
| Symlinked parent directory escaping root on first write | `resolveWritePath` walks the parent chain, resolves the first existing component's symlinks, and rejects writes whose parent escapes `root`. |
| Concurrent writers corrupting a file      | `file_locking: "flock"` takes LOCK_EX on the open fd for each write, cooperating with other flock-aware writers on the host. |
| Agent-mode writes bypassing pagefault's allowlists | **Acknowledged.** Agent mode delegates trust to the subagent — pagefault's `write_paths` do not apply to what the agent writes. Treat `pf_poke` mode:agent callers as users of the configured subagent. |

## Rate limiting, CORS, and the OpenAPI surface (Phase 3)

`server.rate_limit.enabled: true` enables an in-process per-caller token
bucket keyed on the resolved `caller.id`. The limiter sits after the
auth middleware so it can distinguish tokens; anonymous callers
(auth mode `none` or trusted-header fallthrough) share a single bucket
keyed on the literal id `"anonymous"`. Over-budget requests receive
HTTP 429 with a `Retry-After` header and the standard error envelope
(`code: "rate_limited"`). The limiter is per-process — if you run
multiple pagefault instances behind a load balancer, add a shared
rate-limiting layer at the proxy.

`server.cors` is opt-in cross-origin handling. With
`enabled: false` (the default) no CORS headers are emitted and browsers
reject cross-origin requests — fine for loopback and same-origin
deployments. Enabling it with an explicit `allowed_origins` list is the
only way to let a web client (e.g. a Custom GPT in the browser) call
`/api/pf_*` directly.

`GET /api/openapi.json` is **public** (no auth) so importers can fetch
the spec before a bearer token is configured. The document still
advertises `BearerAuth` on every operation, so any actual call to
`/api/pf_*` still requires authentication. Callers that only want the
spec pay no auth cost; callers that want data still go through the
bearer gate.

## Known limitations

- **Naive filesystem scan.** The filesystem backend's `pf_scan` is a
  case-insensitive substring scan across every included file. Fine for
  small trees (hundreds of files); does not scale. Wire a `subprocess`
  backend (ripgrep) or an `http` backend (LCM-style search) alongside
  it for larger corpora.
- **Subagent sandbox is out of scope.** `pf_fault` runs the configured
  agent with whatever privileges the agent already has. See the
  "Subagent safety" section above. `pf_poke` mode:agent shares the same
  trust model — the subagent decides what to write and where, pagefault
  does not re-validate it.
- **Rate limiting is in-process only.** A single pagefault instance
  honours `server.rate_limit`, but if you run several behind a load
  balancer each one keeps its own buckets. Put a shared limiter at the
  proxy if you need global throttling.
- **No per-tool auth.** Any authenticated caller can invoke any enabled
  tool. Per-token tool ACLs are a future addition.
- **No TLS.** Terminate TLS at a reverse proxy (Caddy, nginx,
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
- [ ] If a backend needs `writable: true`, `write_paths` is an
      *explicit* allowlist (never empty), `write_mode` is `append`
      unless you've thought about raw writes, `max_entry_size` is
      set, and `file_locking: "flock"`.
- [ ] `tools.pf_poke: false` on any instance that should stay
      read-only — belt-and-braces alongside per-backend `writable`.
- [ ] `filters.enabled: true` unless you have a specific reason.
- [ ] Audit log path is writable and monitored for disk space.
- [ ] The service is bound to loopback or to a reverse proxy that adds
      TLS and rate limiting.
- [ ] Tokens are rotated after any device loss.
