# Changelog

All notable changes to pagefault are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

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
