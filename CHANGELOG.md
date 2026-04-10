# Changelog

All notable changes to pagefault are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

## 0.5.1 (2026-04-11)

Post-0.5.0 review pass. One real bug (`max_entry_size` was enforced
*after* entry-template wrapping, silently penalising `format: "entry"`
callers by ~40–60 bytes of wrapper overhead and breaking the
documented "raw and entry share one budget" promise), several
documentation/example drifts, and a batch of explicit "known
limitation" notes for issues that don't warrant code changes yet.
No wire or config schema changes — a `pf_poke` caller who was
already staying under the cap sees no difference.

### Fixed
- **`max_entry_size` is now enforced against the raw caller content,
  not the post-wrap body.** Before 0.5.1, `handleWriteDirect` called
  `write.FormatEntry` first and then passed the wrapped bytes to
  `dispatcher.Write` → `FilesystemBackend.Write`, which ran
  `len(content) > maxEntrySize` against the already-wrapped content.
  Net effect: a 1960-byte raw payload in `format: "entry"` failed
  the 2000-byte cap (because the wrapper pushed it to ~2020), even
  though the 0.5.0 docstring on `model.ErrContentTooLarge` and
  `docs/security.md` §Write safety both promised the cap was
  measured on the raw content. The fix:
  - Added `MaxEntrySize() int` to the `writableBackendAccessor`
    interface in `internal/tool/write.go`.
  - `handleWriteDirect` now peeks the backend once, checks
    `len(in.Content) > be.MaxEntrySize()` **before** calling
    `FormatEntry`, and returns `ErrContentTooLarge` if over.
  - `FilesystemBackend.Write` dropped its own `maxEntrySize`
    check — the backend now exposes the limit via the accessor but
    does not itself reject oversize writes, because by the time
    content arrives the raw/wrapped distinction is lost.
  - `TestHandleWrite_DirectContentTooLarge` rewritten to use a raw
    payload that exceeds the cap (the old test exploited the wrap
    overhead — the exact bug this fix removes). New test
    `TestHandleWrite_DirectContentAtCapSucceeds` guards the
    regression: a 10-byte raw payload into a 10-byte cap now passes
    and writes a ~60-byte wrapped entry, as intended.
  - `TestFilesystem_Write_MaxEntrySize` renamed to
    `TestFilesystem_Write_MaxEntrySizeNotEnforcedAtBackend` and
    inverted — it now proves the backend accepts over-cap content
    and that enforcement moved to the tool layer.

### Changed (docs)
- **`docs/security.md` §Audit** — corrected the `bytes` field
  description. The audit log records the bytes passed to the
  backend (for `format: "entry"` that includes the wrapper), not
  a pre-wrap "raw content byte count" as 0.5.0 claimed. The
  enforcement promise is now on `max_entry_size` (raw bytes, tool
  layer); the audit field is just "bytes on the wire to the
  backend".
- **`docs/security.md` §Write safety** — added a "Known limitation"
  block under *Sandbox for new files* documenting the TOCTOU race
  between `resolveWritePath`'s symlink check and the subsequent
  `MkdirAll` + `OpenFile`. Deferred to whenever pagefault grows a
  multi-tenant deployment story; the single-operator trust model
  already puts the attacker on the wrong side of the sandbox.
- **`docs/security.md` §Audit** — added an explicit note that
  `mode: "agent"` writes surface as `tool: "pf_fault"` in the audit
  log (because the work is done by `dispatcher.DeepRetrieve`, which
  emits its own audit entry). Operators auditing *all* writes must
  scan both `pf_poke` and `pf_fault` rows. Emitting a duplicate
  `pf_poke` row per agent call was considered but rejected — the
  underlying action really is a subagent spawn, not a direct write.
  Revisit when structured subagent responses ship in Phase 5.
- **`docs/security.md` §Mode: agent** — flagged
  `targets_written` as "reserved but always absent" until Phase 5
  ships structured subagent responses.
- **`docs/api-doc.md` §pf_poke** — same `targets_written`
  clarification in the mode:agent response section; corrected the
  Security Notes bullet that claimed the backend enforces
  `max_entry_size` (it's the tool layer now).
- **`docs/config-doc.md`** — `write_paths` now explicitly calls out
  the URI-scheme footgun: unlike `include` (relative paths), these
  patterns must be full URIs (`memory://notes/*.md`). A scheme-less
  `notes/*.md` silently matches nothing. Also documented that
  `max_entry_size: 0` is *not* "unlimited" — `applyWriteDefaults`
  rewrites it to the 2000-byte safe default whenever `writable:
  true`; callers who really want no cap must set a very large
  number.
- **`plan.md`** — error-case table for `pf_poke` mode:agent now
  correctly describes timeouts as `200 OK` with `timed_out: true`
  (matching the `pf_fault` success-envelope pattern), not a `504`.
  Also added a note that `targets_written` is reserved for Phase 5.
- **`configs/example.yaml`** — removed the "Phase 4, not yet
  implemented" comment on `pf_poke`, turned it on by default, and
  added a commented-out Phase-4 write block on the `fs` backend
  (with the URI-scheme caveat spelled out inline). The `filters`
  section gained a commented-out `path.write_allow`/`write_deny`
  example.
- **`internal/write/writer.go`** — `WriteModeAny` docstring no
  longer claims to permit prepend and overwrite. As of 0.5.1 the
  only observable effect of `any` is unlocking `format: "raw"` on
  `pf_poke`; prepend and overwrite operations are reserved but
  not implemented (the `Writer` interface only exposes `Append`).
  `internal/config/config.go` and `docs/config-doc.md` updated to
  match.
- **`internal/model/model.go`** — `ErrContentTooLarge` docstring
  updated to describe the new enforcement site (tool layer,
  before wrap) and to point at the handler for the checked bytes.

### Deferred (documented, not fixed)
- `resolveWritePath` TOCTOU (see §Write safety). Single-operator
  threat model makes it academic; a real fix needs
  `openat(O_NOFOLLOW)`.
- Agent-mode audit gap (appears as `pf_fault`, not `pf_poke`).
- `targets_written` always absent. Waits on structured subagent
  responses.
- Prepend/overwrite under `write_mode: "any"`. Not in Phase 4
  scope; `Writer` interface would need new methods.

## 0.5.0 (2026-04-11)

Phase 4 — writeback. `pf_poke` ships, the filesystem backend gains
optional write support behind five independent gates, and the write
path gets its own filter layer. Every item from `plan.md` §10 Phase 4
shipped. The bump is minor because Phase 3 clients are unaffected —
read-only deployments see no behavior change.

### Added
- **`pf_poke` tool.** The write counterpart to `pf_peek`. Two modes:
  - **`direct`** — filesystem append. The backend enforces its own
    `write_paths` allowlist, `write_mode` (append-only vs. any
    mutation), and `max_entry_size` cap. The tool layer wraps content
    via `write.FormatEntry` in `format: "entry"` mode (a
    newline-delimited, horizontal-ruled, timestamped markdown block),
    or passes it through unchanged in `format: "raw"` mode (which
    additionally requires `write_mode: "any"` on the target backend
    as a second-tier opt-in). The raw caller content is measured
    against `max_entry_size` *before* entry-template wrapping, so
    `raw` and `entry` share one byte budget.
  - **`agent`** — delegate to a subagent. Routes through the same
    `dispatcher.DeepRetrieve` machinery `pf_fault` uses. Composes a
    natural-language task ("A remote agent (<caller>) wants to record
    … Target: … Read the relevant memory files, decide the best
    location, and write it appropriately") and flattens timeouts
    into a success envelope with `timed_out: true` + partial stdout.
    **Trust is delegated to the subagent**: pagefault's `write_paths`
    and `filters.path.write_*` do *not* apply to what the agent
    writes — see `docs/security.md` §Write safety.
- **`internal/write` package.** New `Writer` interface plus
  `FilesystemWriter` with POSIX advisory locking. `Append` takes
  LOCK_EX via `syscall.Flock` on the open fd, does an
  `O_APPEND|O_WRONLY|O_CREATE` write, and fsyncs before returning.
  `file_locking: "none"` falls back to a per-writer mutex for
  single-writer environments. `FormatEntry` handles the `entry` /
  `raw` templating with an injectable `Clock` so tests can pin the
  timestamp.
- **`backend.WritableBackend` interface.** Optional extension to
  `Backend` with `Writable() bool` and
  `Write(ctx, uri, content) (int, error)`. The dispatcher type-asserts
  to this before routing a write — a backend that does not implement
  it (or implements it but returns `Writable() == false`) fails the
  write with `ErrAccessViolation` before any syscall.
- **`FilesystemBackend` gains write support.** When
  `writable: true` is set in config, the constructor wires a
  `FilesystemWriter` and the backend implements `WritableBackend`.
  `Write` enforces: (a) backend writability, (b) the read
  `include`/`exclude` filter (writes inherit the read visibility),
  (c) the per-backend `write_paths` allowlist, (d) `max_entry_size`
  on the raw content, (e) sandbox-safe path resolution for
  not-yet-existing leaves via `resolveWritePath`, which walks the
  parent chain to find the first existing component and verifies
  its symlink-resolved path stays under `root`. This catches the
  case where `root/notes` is a symlink to `/etc` and the write is
  `memory://notes/leak.md` — new-file writes no longer escape the
  sandbox via a cold-cache parent symlink.
- **`dispatcher.Write` method.** Server-wide write filter →
  scheme-based backend lookup → `WritableBackend` type assertion →
  backend.Write. Audit entries tag `tool: "pf_poke"` with the URI
  and the raw content byte count (never the content body itself).
- **`PathFilter` write layer.** `filters.path.write_allow` and
  `filters.path.write_deny` are Phase-4 additions to
  `PathFilterConfig`. When at least one is set, writes are checked
  exclusively against the write pair; when both are empty, writes
  fall through to the read `allow`/`deny` pair. The `Filter`
  interface gains an `AllowWriteURI` method; every built-in filter
  implements it (`TagFilter`/`RedactionFilter` are pass-through).
- **`FilesystemBackendConfig` write fields.** `Writable`,
  `WritePaths`, `WriteMode` (`append` / `any`), `MaxEntrySize`,
  `FileLocking` (`flock` / `none`). `applyWriteDefaults` fills in
  safe defaults (`append`, 2000 bytes, `flock`) when `Writable` is
  `true` and the operator omits the rest, so `writable: true` alone
  is still a safe config.
- **`ToolsConfig.PfPoke`** — the enable toggle, defaults to enabled.
- **`model.ErrContentTooLarge` sentinel.** Mapped to HTTP 413 and
  the stable error code `content_too_large`. Added to the server's
  `errorCode`/`errorStatus` switches and to the shared error-code
  table test.
- **`pf_poke` registered across every transport.** MCP tool schema
  in `internal/tool/mcp.go`, REST route at `/api/pf_poke` in
  `internal/server/server.go`, OpenAPI spec in
  `internal/server/openapi.go` (with new `WriteInput` / `WriteOutput`
  schemas and a `413 content_too_large` response on every operation),
  and CLI subcommand `pagefault poke --mode direct|agent
  [--uri URI] [--format entry|raw] [--agent ID] [--target HINT]
  [--timeout N] <content...>`. When no positional content is given
  the CLI reads from stdin, so
  `echo "fixed auth bug" | pagefault poke --mode direct --uri memory://notes/today.md`
  works.
- **Landing page lists `/api/pf_poke`.** `GET /` on the server now
  mentions the new endpoint.

### Changed
- **`dispatcher.BackendForURI` exported.** The tool layer's
  format-raw pre-flight check needs to peek at the backend's
  `WriteMode` before calling `dispatcher.Write`; exporting
  `BackendForURI` keeps the scheme-parsing logic in one place.
- **`api-doc.md` title** bumped to "Phase 1–4"; the "Planned" section
  no longer lists `pf_poke`. New `pf_poke` section documents both
  modes with request/response shapes and error cases.
- **`plan.md` §10 Phase 4** collapsed into a shipped-summary paragraph
  pointing at this changelog entry. §14 intro updated to reflect
  "Phases 1, 2, 3, and 4 have shipped".
- **`docs/security.md` §Write safety rewritten.** Previously a forward
  reference to Phase 4 — now the canonical write threat model with a
  five-gate description, entry-template rationale, sandbox-for-new-files
  explanation, and agent-mode trust delegation. Threat table and
  deployment checklist pick up write-specific rows.
- **`docs/architecture.md`** filter pipeline diagram gains a write
  branch; `internal/write` added to the package map; future phases
  list updated.
- **`docs/config-doc.md`** `filesystem` backend section documents
  every write field with defaults + rationale, and ships a realistic
  "personal memory write config" example. The `filters` section
  documents `path.write_allow`/`write_deny` and the read-broadly /
  write-narrowly pattern. Order-of-operations note lists the new
  write URI check.

### Tests
- **Full Phase-4 coverage.** `internal/write/writer_test.go`
  (happy path, concurrent appends, parent-dir creation, cancelled
  context, default lock mode); `internal/write/format_test.go`
  (fixed-clock templating, entry vs. raw, empty-label stripping,
  trailing newline normalisation); `internal/backend/filesystem_write_test.go`
  (happy path, read-only rejection, include-vs-write_paths
  interactions, max_entry_size, path traversal, symlinked-parent
  escape, accessors); `internal/filter/filter_test.go` (new
  AllowWriteURI table tests for fallback + write-list + write-deny
  behavior); `internal/dispatcher/dispatcher_test.go` (writable mock
  backend + Write happy path + all rejection branches +
  BackendForURI); `internal/tool/write_test.go` (direct + agent +
  format raw + filter deny + content too large + entry template);
  `internal/tool/mcp_test.go` (pf_poke MCP registration +
  missing-arg paths); `internal/server/server_test.go` (REST
  `/api/pf_poke` + 413 envelope + OpenAPI includes pf_poke + root
  landing page mentions pf_poke + ErrContentTooLarge in the shared
  code-mapping table); `cmd/pagefault/tools_test.go` (runPoke text +
  JSON + missing URI + empty stdin + read-only backend rejection).

## 0.4.1 (2026-04-11)

Review-response patch for 0.4.0. Cross-review between an independent
reviewer and a follow-up audit surfaced six items; the ones with a
concrete contract or behaviour impact are fixed here, the rest are
documented under "Deferred" below.

### Fixed
- **`pf_load` output `format` field no longer lies when the context
  default is non-markdown.** `HandleGetContext` previously fell back to
  a hard-coded `"markdown"` string when the caller left `format` empty,
  but the dispatcher was actually resolving against the context's
  configured default — so a context with `format: json` would return
  valid JSON content paired with `format: "markdown"` in the response
  envelope. `dispatcher.GetContext` now returns the resolved format as
  a second value (signature: `(content, format, skipped, err)`) and
  the handler echoes that back. Regression covered by
  `TestHandleGetContext_EchoesResolvedFormat`.
- **CORS preflight no longer short-circuits disallowed origins.** The
  previous middleware returned `204 No Content` for any
  `OPTIONS + Access-Control-Request-Method` request regardless of
  whether the origin was allowed, which (a) leaked a uniform 204 for
  every path and (b) silently bypassed the downstream auth / route
  chain. The short-circuit is now gated on `originAllowed(origin)`;
  disallowed preflights fall through to the normal handler chain.
  Regression covered by
  `TestCORS_DisallowedOriginPreflightFallsThrough`.
- **`pf_load` MCP tool description now lists all three formats.** The
  MCP schema advertised only `'markdown' | 'json'`; the OpenAPI spec,
  REST docs, and CLI help already listed
  `markdown-with-metadata` as well. MCP clients get the full menu now.
- **Rate-limit 429 envelope flows through the shared
  `writeError` path.** Previously the rate-limit middleware built its
  own `errorEnvelope{Code: "rate_limited"}` literal; the new
  `model.ErrRateLimited` sentinel lets the middleware call
  `writeError(w, 429, …ErrRateLimited)` and re-use the same code mapping
  as every other sentinel. `errorCode` / `errorStatus` gained a case;
  `TestErrorCodeMapping` is a direct table test so the REST envelope and
  sentinel list stay in lockstep.
- **Dead `lastSeen` field removed from `rateLimiter`.** The map was
  written to on every `limiterFor` call but never read — leftover from
  a planned GC that never landed. The file's package doc now states the
  eviction decision explicitly so the next maintainer doesn't re-add it
  by accident.
- **Root landing page (`GET /`) mentions `/api/openapi.json`.** The new
  public spec endpoint was listed in the README and docs but missing
  from the `GET /` help text.

### Deferred
These items were raised in review but intentionally not fixed in this
patch. Documenting here so they stay on the radar:
- **Rate-limiter bucket GC.** Even after dropping `lastSeen`, the
  `buckets` map grows unboundedly with distinct caller ids. In the
  bearer-token deployment shape this is bounded by the tokens file
  (one bucket per device plus "anonymous"), so there is no leak in
  practice. A `trusted_header` deployment could drift — add a
  background eviction pass keyed on last-seen time when that shape is
  actually used. Docstring in `ratelimit.go` points here.
- **CORS middleware scope.** Reviewer suggested tightening
  `r.Use(corsMiddleware(…))` to apply only to `/api/*`. We keep it at
  the root because ChatGPT Custom GPT Actions fetches
  `/api/openapi.json` cross-origin, and a dashboard polling `/health`
  from a different origin needs CORS headers too. Public endpoints
  getting CORS headers is the intended behaviour, not a bug.
- **JSON context format re-marshal is O(n²) in source count.** Dropping
  a tail source and re-marshaling the whole bundle is `O(n²)` bytes
  worst case. Contexts currently ship with ≤ 5 sources so the constant
  wins, but if a multi-hundred-source context becomes real, switch to a
  "build forward, stop at budget" pass.
- **`/health` probes run sequentially.** Each backend gets its own 2s
  timeout; five slow backends can stretch a health probe to 10s. Fix is
  a bounded parallel fan-out — not urgent while every shipping backend
  is cheap to probe.
- **JSON format duplicates `tags` between the source top-level and the
  nested `metadata` object.** Strip `tags` from the metadata copy to
  deduplicate. Cosmetic; clients that iterate `metadata` may see the
  redundant key.
- **Public endpoints (`/health`, `/`, `/api/openapi.json`) are not
  rate-limited.** Intentional — `/health` must stay flood-safe for
  liveness probes and the OpenAPI spec is cached client-side by
  importers. Revisit only if one of these endpoints starts doing real
  work.

### Changed
- **`dispatcher.GetContext` signature:**
  `(string, []SkippedSource, error) → (string, string, []SkippedSource, error)`.
  The new second return is the resolved format. Internal API;
  `internal/tool/get_context.go` is the only caller and has been
  updated along with the dispatcher tests.
- **`model.ErrRateLimited` sentinel added.** Used by the rate-limit
  middleware and by the REST `errorCode`/`errorStatus` switches.

## 0.4.0 (2026-04-11)

Phase 3 — polish and production. Every item from `plan.md` §10 Phase 3
shipped except for the Phase-4 write-path work tracked separately.

### Added
- **`RedactionFilter`.** Regex-based content masking in the
  `FilterContent` stage, driven by the existing
  `filters.redaction.{enabled,rules}` config shape. Rules are compiled
  at server start so bad patterns fail fast; capture groups (`$1`,
  `$2`, …) are supported in the replacement string. Runs after the
  path and tag checks so the un-redacted copy never leaves the
  dispatcher.
- **JSON and markdown-with-metadata context formats.** `pf_load.format`
  now accepts `markdown` (default, unchanged), `markdown-with-metadata`
  (adds a per-source blockquote with content-type and tags), and
  `json` (a structured `{"name":..., "sources":[...]}` bundle). JSON
  mode enforces `max_size` by dropping sources from the tail — the
  emitted document is always valid JSON, and the dropped URIs appear
  in `skipped_sources` with `reason: "max_size budget exceeded"`.
- **`/api/openapi.json` — live OpenAPI 3.1.0 spec.** Public (no auth)
  so importers like ChatGPT Custom GPT Actions can fetch the schema
  before a bearer token is supplied. The document is generated from
  the current config, respects per-tool enable/disable toggles, and
  advertises the `BearerAuth` scheme + the structured `ErrorEnvelope`
  on every operation. `server.public_url` is echoed in `servers[0]`.
- **`server.cors` config.** Opt-in cross-origin handling for the REST
  transport. Supports an explicit origin allowlist (or `"*"`),
  `allow_credentials`, preflight short-circuiting, and `max_age`.
  Disabled by default — no headers emitted until
  `server.cors.enabled: true` and `allowed_origins` is non-empty.
- **`server.rate_limit` config.** Per-caller in-process token bucket
  keyed on `caller.id`. Over-budget callers receive HTTP 429 with a
  `Retry-After` header and the structured error envelope (`code:
  "rate_limited"`). Anonymous callers share a single bucket. Defaults
  to 10 rps / 20 burst when enabled.
- **`HealthChecker` backend interface.** Optional — backends that
  implement it are probed by `/health` with a 2 s timeout. The
  filesystem backend ships a probe that stats its resolved root so
  deleted / unmounted volumes surface as `"unavailable"`.
- **Richer `/health` envelope.** Per-backend entries are now
  `{"status": "ok"|"unavailable", "error"?: "..."}`; top-level status
  is `"ok"` / `"degraded"` / `"unavailable"` depending on how many
  backends are up. Always returns HTTP 200 so liveness probes stay
  simple.
- **README client setup guides.** Short copy-paste sections for
  Claude Code (`claude mcp add …`), Claude Desktop (JSON config),
  and ChatGPT Custom GPT Actions (import `/api/openapi.json`).

### Changed
- **Structured error envelope for every non-2xx REST response.**
  Migrated from `{"error": "bad request", "message": "..."}` to
  `{"error": {"code": "invalid_request", "status": 400, "message":
  "..."}}`. Each sentinel error maps to a stable snake_case `code`
  (`invalid_request`, `unauthenticated`, `access_violation`,
  `resource_not_found`, `context_not_found`, `backend_not_found`,
  `agent_not_found`, `backend_unavailable`, `subagent_timeout`,
  `rate_limited`, `internal_error`). `internal/auth/auth.go`'s
  `writeAuthError` emits the same shape. **Breaking for REST
  clients that parsed the previous envelope shape** — MCP clients
  are unaffected (they use mcp-go's tool-result error path).
- **`docs/config-doc.md`, `docs/api-doc.md`, `docs/security.md`** all
  updated for the new surface area (CORS, rate limit, context
  formats, structured errors, OpenAPI endpoint, live Redaction
  filter). Phase-3 "planned" wording removed where the feature now
  exists.
- **`plan.md` §10 Phase 3** collapsed into a shipped-summary paragraph
  pointing at this changelog entry.

### Dependencies
- `golang.org/x/time v0.15.0` added for the rate-limiter's
  `rate.Limiter` (direct dependency).

## 0.3.2 (2026-04-10)

Review-response patch for 0.3.0 / 0.3.1. Targeted fixes from an
independent reviewer plus documentation cleanup. No wire-surface
changes.

### Changed
- **HTTP backend surfaces missing `response_path` as an error.**
  `decodeHTTPSearchResults` previously returned `(nil, nil)` when a
  configured `response_path` was absent from the body, which silently
  masked operator typos as "no results". It now returns a wrapped
  `model.ErrBackendUnavailable` (→ HTTP 502) with a message naming the
  missing path. The "non-array path" branch is now also wrapped in the
  same sentinel for consistency, so both config-error cases propagate
  the same status.
- **`hasAgent` deduplicated across subagent backends.** `subagent_cli.go`
  and `subagent_http.go` each had a private method that linear-scanned
  `b.agents` for an id. Extracted to a package-private
  `hasAgentID(agents, id) bool` helper in `subagent.go`. Behaviour
  unchanged; the constructors' uniqueness check was already authoritative.
- **`SubprocessBackendConfig.Parse` docstring** now lists all three parse
  modes (`ripgrep_json`, `grep`, `plain`). The `grep` mode was already
  implemented and tested — only the doc comment was stale.

### Fixed
- **README, CLAUDE.md, and api-doc.md version drift.** Several places
  still referenced `0.3.0` (CLAUDE.md directory-tree comment, api-doc.md
  `/health` example) or described the tool surface as "Phase 1 / four
  tools" from the pre-Phase-2 state. Updated to reflect Phase 1-2 and
  six tools.
- **README Recent Changes trimmed to the three most recent releases**
  (`0.3.2` / `0.3.1` / `0.3.0`), per the CLAUDE.md house rule.
  Full history still lives in `CHANGELOG.md`.

### Deferred
- `subprocess` backend's `plain` parse mode still returns empty-string
  URIs. A reviewer suggested either a `uri_template` config field or
  an `unknown://<n>` fallback; both are Phase-3 scope (`plain` is
  explicitly documented as "smoke-test only — pick a structured mode
  for real use").

## 0.3.1 (2026-04-10)

Review-response patch for 0.3.0. No wire-surface or behavior changes —
only tests, refactoring, and documentation.

### Added
- **`configs/example.yaml`** — documented reference config showing all
  five backend types (filesystem, subprocess, http, subagent-cli,
  subagent-http). Ships runnable (uses echo-based subagent-cli) with
  subprocess / http / subagent-http commented out so operators can
  uncomment and edit. README + `docs/config-doc.md` updated to point
  at it.
- **Direct unit tests for every `Decode*Backend` helper in
  `internal/config`**: `TestDecodeSubprocessBackend_*`,
  `TestDecodeHTTPBackend_*`, `TestDecodeSubagentCLIBackend_*`,
  `TestDecodeSubagentHTTPBackend_*` (happy path, wrong type, missing
  required fields). Coverage for `internal/config` bounced from
  54.6% → 87.6%.
- **`internal/backend/http_helpers_test.go`** — focused unit tests for
  the shared helpers, including `extractResponse` which was only
  exercised indirectly before (empty-path raw path, string/number/
  object leaves, missing paths, malformed JSON, `$.` prefix).

### Changed
- **Shared HTTP helpers extracted to `internal/backend/http_helpers.go`.**
  `renderTemplate`, `jsonEscape`, `walkPath`, and `extractResponse`
  were declared in `subagent_http.go` but used by both
  `subagent_http.go` and `http.go`, which made ownership fuzzy.
  They now live in a dedicated file so neither consumer is "reaching
  across" its peer. `subagent_http.go`'s `encoding/json` import was
  dropped along the way.

## 0.3.0 (2026-04-10)

### Added
- **Phase 2 subagents and new backend types.** Four new backends land at
  once, along with the tools that use them:
  - `subagent-cli` — spawns a CLI agent via `exec.CommandContext`, with
    `{agent_id}` / `{task}` / `{timeout}` placeholders, tokenized argv
    (no shell), per-call timeouts that kill the process on expiry, and
    partial-stdout capture on timeout.
  - `subagent-http` — POSTs to a configured endpoint, JSON body
    template, bearer auth, dotted `response_path` extraction, per-call
    timeout via `http.Request` context.
  - `subprocess` — generic command-runs-a-query backend. Parse modes:
    `ripgrep_json`, `grep` (`path:lineno:content`), `plain`. Treats
    `exit 1` as "no matches" instead of an error.
  - `http` — generic HTTP search backend. POST/GET, body template,
    `response_path`, per-request-object results with `uri`/`snippet`/
    `score`/`metadata`.
- **`pf_fault` tool.** The real page fault: a tool that spawns a
  subagent to answer a natural-language query. Spec in `docs/api-doc.md`
  §pf_fault — query + agent + timeout_seconds in, answer/partial
  result + elapsed + timed_out flag out. Timeouts surface as a
  successful response with `timed_out: true` so clients can inspect the
  partial result without special error handling.
- **`pf_ps` tool.** Lists every agent exposed by every configured
  `SubagentBackend` (id, description, backend name), ps-style.
- **CLI subcommands.** `pagefault fault <query…>` (flags: `--agent`,
  `--timeout`) and `pagefault ps` round out the CLI surface to match
  the wire tools. Same dispatcher, same filter + audit path as REST/MCP.
- `internal/model`: two new sentinel errors — `ErrAgentNotFound` and
  `ErrSubagentTimeout`. The server maps them to `404 Not Found` and
  `504 Gateway Timeout` respectively.
- `internal/backend.SubagentBackend` interface: extends `Backend` with
  `Spawn(ctx, agentID, task, timeout) (string, error)` and
  `ListAgents() []AgentInfo`.
- `dispatcher.ListAgents` and `dispatcher.DeepRetrieve` methods, plus
  `dispatcher.findSubagent` which picks the first configured backend
  when no agent id is specified and scans every backend otherwise.

### Changed
- `buildDispatcher` in `cmd/pagefault/serve.go` now wires the four new
  backend types. Only `filesystem`, `subprocess`, `http`,
  `subagent-cli`, `subagent-http` are recognised — unknown types fail
  with a helpful error listing the valid names.
- `internal/server`: `errorStatus` maps `ErrAgentNotFound` → 404 and
  `ErrSubagentTimeout` → 504. REST routes `/api/pf_fault` and
  `/api/pf_ps` added behind the same auth + filter stack as the
  Phase-1 tools.
- `internal/tool/mcp.go`: registers the two new MCP tools
  (`pf_fault`, `pf_ps`) via `registerDeepRetrieve` / `registerListAgents`.

## 0.2.0 (2026-04-10)

### Added
- **CLI subcommands for every Phase-1 tool.** `pagefault maps` / `load <name>` / `scan <query>` / `peek <uri>` drive the same `HandleX` functions as the HTTP/MCP transports, so you can exercise a config locally without starting the server. Common flags: `--config`, `--no-filter`, `--json`. Per-command flags: `load --format`, `scan --limit/--backends`, `peek --from/--to`. Config lookup falls back `--config → $PAGEFAULT_CONFIG → ./pagefault.yaml`. Positional args can appear anywhere on the command line (a local `parseInterspersed` helper hoists flags past positionals before handing off to stdlib `flag`). Filters apply by default; `--no-filter` is the operator escape hatch.
- `audit.mode: stderr` — new audit sink that writes JSONL to stderr. The CLI subcommands auto-rewrite `stdout` → `stderr` so data streams stay pipe-clean (`pagefault load demo --json | jq .`).
- `docs/security.md`: threat model, auth/filter/audit notes, known limitations, deployment checklist. Plan §2 required this doc; it was missing from the initial Phase-1 commit.
- `pf_load` now returns a `skipped_sources` field listing any configured sources that were dropped (blocked by filter or backend read error), each with a `reason`. Skips are also logged at WARN level via `log/slog`, so silent "partial context" failures are now observable.
- Test coverage: `internal/tool` 45.5% → 91.9% (direct unit tests for `toolResultJSON`/`toolResultError` and MCP registration handlers via `MCPServer.GetTool`), `cmd/pagefault` 39.6% → 68.6% (tests for `buildDispatcher`, `runTokenList`, `resolveTokensFile`, plus a full `tools_test.go` suite exercising all four CLI subcommands, text + JSON + env/cwd fallback + `--no-filter` + stdout-audit redirect + positional-before-flags).

### Changed
- Repo and Go module renamed from `page-fault` to `pagefault` for consistency with the binary, CLI, and product name. Go module path is now `github.com/jet/pagefault`. Two-word "page fault" references to the OS concept in explanatory prose are preserved intentionally.
- **Tool names renamed to a `pf_*` scheme.** The wire surface (MCP tool names, REST routes `/api/...`, config `tools:` keys, audit log `tool` field) now uses page-fault-themed names: `list_contexts` → `pf_maps`, `get_context` → `pf_load`, `search` → `pf_scan`, `read` → `pf_peek`, `deep_retrieve` → `pf_fault`, `list_agents` → `pf_ps`, `write` → `pf_poke`. Internal Go handler/type names (`HandleListContexts`, `GetContextInput`, etc.) and `internal/tool/*.go` file names keep their generic form; the wire↔code mapping is documented in `CLAUDE.md` §Tool Naming. This is a breaking change to the HTTP/MCP contract — clients that were wired against the old names need to update.
- `dispatcher.GetContext` now returns `(content, skipped, err)` instead of `(content, err)`. Internal API; callers in `internal/tool/get_context.go` and tests updated.

### Fixed
- `pf_load` truncation no longer splits UTF-8 multi-byte characters. The byte-level cut point is walked back to the nearest rune boundary before appending the `[truncated]` marker, so contexts with non-ASCII content (e.g. CJK) stay valid.
- `requestLogger` middleware in `internal/server` was reading a stale `middleware.RequestIDKey` context value and discarding it via `_ = start` — dead code left over from a chi logger template. It now measures request duration with `time.Now()` and logs `duration_ms` alongside status and bytes.

## 0.1.0 (2026-04-10)

### Added
- Initial project scaffold and Phase 1 MVP
- Filesystem backend with glob include/exclude, sandbox, auto-tag, URI scheme mapping
- Config package: YAML loader with ${ENV} substitution and validation
- Auth package: BearerTokenAuth (JSONL tokens file) and NoneAuth
- Filter package: PathFilter (allow/deny globs) and TagFilter
- Audit package: JSONL audit logger
- Tool dispatcher with pre/post filter pipeline
- Phase-1 tools: `list_contexts`, `get_context`, `search`, `read`
- HTTP server: chi router with MCP (`/mcp`) and REST (`/api/{tool}`) transports
- CLI: `pagefault serve`, `pagefault token create/ls/revoke`, `pagefault --version`
- Minimal config example and demo data
- Initial docs: api-doc.md, config-doc.md, architecture.md, CLAUDE.md
