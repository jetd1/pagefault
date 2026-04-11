# Changelog

All notable changes to pagefault are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

## 0.11.3 (2026-04-12)

The embedded landing site is now auto-deployed to GitHub Pages so
the README can link to a live preview. No binary or wire-surface
changes; this is strictly deploy-time infrastructure.

### Added

- **`.github/workflows/pages.yml`** — GitHub Actions workflow that
  builds and deploys the static landing site to GitHub Pages on
  every push to `main` that touches `web/**` or `VERSION`, plus a
  `workflow_dispatch` hook for manual runs. Implementation:
  1. Checkout at the commit being pushed.
  2. Copy `web/index.html`, `web/styles.css`, `web/script.js`,
     `web/favicon.svg`, `web/icons.svg` into a `_site/` build
     directory.
  3. `sed` replace the `{{version}}` sentinel in
     `_site/index.html` against the trimmed contents of
     `VERSION` — the same substitution
     `internal/server.New` does at binary startup via
     `bytes.ReplaceAll`, just running in a different place
     because GitHub Pages serves static files as-is.
  4. Fail the job loudly if any literal `{{version}}` survives
     substitution (defends against adding a new sentinel site
     without updating the workflow).
  5. Upload + deploy via the official
     `actions/configure-pages@v5` +
     `actions/upload-pages-artifact@v3` +
     `actions/deploy-pages@v4` chain.
  Concurrency is `group: pages, cancel-in-progress: false`, so a
  rapid-fire push sequence still deploys the latest state.
  Permissions are the minimum Pages needs: `contents: read`,
  `pages: write`, `id-token: write`.

- **README live-preview link.** The README header nav strip now
  has a `live preview ↗` link pointing at
  `https://jetd1.github.io/pagefault/`. The sub-line under the
  strip explains the auto-deploy pipeline and links to the
  workflow file. The "Landing site" row in the At-a-glance
  table was expanded to mention both serving contexts — the
  binary and GitHub Pages.

- **Design system surfaces table.** `docs/design.md` §11 now
  lists the landing site twice: once under the binary
  (`web/` + `internal/server/server.go`, runtime substitution)
  and once under GitHub Pages (`web/` +
  `.github/workflows/pages.yml`, CI substitution). Single
  source of truth, two render contexts, both substitute the
  sentinel.

### Setup note

GitHub Pages must be enabled before the workflow can deploy:
**Settings → Pages → Source: "GitHub Actions"**. This is a
one-time manual step per repository; subsequent pushes will
deploy automatically. Until Pages is enabled, the
`actions/deploy-pages@v4` step will error; the build and
substitution steps preceding it still succeed, so the diff can
be reviewed end-to-end before the Pages setting is flipped.

### Not changed

- No Go code, no binary behaviour, no wire format.
- The `web/` source files are identical to 0.11.2 — the sentinel
  still lives in `web/index.html` as it did before.
- `internal/server/server.go`'s startup substitution still runs
  for the binary-served case; the CI substitution runs for
  GitHub Pages. Both render the same result against the same
  `VERSION` file.
- The GitHub hosting URL (`github.com/jetd1/pagefault`) and the
  Go module path (`jetd.one/pagefault`) are unchanged.

## 0.11.2 (2026-04-12)

Documentation-only patch. The top-level README has been reorganized
and visually tightened; no code, config, wire formats, CLI verbs, or
tool semantics have changed. Every factual claim, command, version
gate, and URL in the new README was preserved verbatim from the
0.11.1 README — only structure, hierarchy, and presentation were
edited.

### Changed

- **`README.md` overhaul.** Restructured for readability without
  removing content:
  - Centered `<h1>` / tagline / nav strip at the top, linking to
    the docs and changelog directly.
  - Tight 3-sentence intro followed by an ASCII `fault → handler
    → resolved` flow diagram that mirrors the landing page's
    concept section, replacing the previous ~25-line wall of
    version history in prose form.
  - **Contents** table of navigation anchors.
  - **At a glance** summary table — tools / transports / backends
    / auth modes / OAuth2 grants / filters / observability /
    runtime / landing site — so a new reader can size pagefault
    up without scrolling.
  - New **Tools** table (wire · CLI · does · Unix analog) mirroring
    the landing page's tools table, moved up into the main
    narrative rather than buried in the intro paragraph.
  - New **Transports** table (transport · route · primary use).
  - **Clients** section restructured: Claude Code stays flat, the
    recommended PKCE flow stays front-and-center under Claude
    Desktop, and the `client_credentials` fallback plus the
    `supergateway` legacy bridge are collapsed behind `<details>`
    so the main flow reads cleanly. A fourth `<details>` block
    now holds a compact Claude Desktop support history table
    (0.6.0 → 0.9.0), replacing the free-form "History" blockquote
    at the bottom of the old section.
  - **Production config** (renamed from "Creating a production
    config") with the same numbered walkthrough.
  - **Development** (renamed from "Tests and linting") expanded
    to list every `make` target documented in CLAUDE.md's quick
    reference, and now points at CLAUDE.md for the full developer
    guide.
  - **Documentation** list converted to a table and gained a row
    for `docs/design.md` (added in 0.11.0 but previously
    un-linked from the README).
  - Recent Changes now lists 0.11.2, 0.11.1, 0.11.0 — 0.10.1
    rotated out per the three-most-recent policy in CLAUDE.md.

### Not changed

- Every command, flag, route, port, environment variable, version
  gate, and URL in the README is identical to the 0.11.1 version.
- No binary code, server routing, auth logic, tool wiring, wire
  format, YAML config key, or test assertion was touched.

## 0.11.1 (2026-04-12)

Two follow-ups to 0.11.0 bundled into one patch release — both of
them flushing out pre-existing 0.11.0 bugs that came from conflating
identifiers that were supposed to be distinct:

1. **Landing-page version badge is now live.** 0.11.0 shipped with
   `v0.10.1` hardcoded in the nav badge, the footer badge, and the
   quickstart `pagefault --version` snippet — a cosmetic bug caused
   by me forgetting to bump hand-written HTML on the release. Fixed
   by introducing a `{{version}}` sentinel the server substitutes
   at startup against the injected `Version` variable.
2. **Go module path renamed to a vanity path** — `jetd.one/pagefault`
   replaces `github.com/jet/pagefault`. This corrects the
   module-path / hosting-URL confusion that caused me to wrongly
   write `github.com/jet/pagefault` into the landing site's GitHub
   hyperlinks during 0.11.0, and establishes a stable vanity
   identifier the module can live under permanently.

No runtime behaviour changes: every binary, config key, tool, HTTP
endpoint, CLI verb, audit-log field, and wire format continues to
work identically.

### Fixed

- **Landing page version badge no longer drifts from `VERSION`.**
  `web/index.html` now carries a `{{version}}` sentinel in the
  three places where the version string appears — the nav badge,
  the footer badge, and the `pagefault --version` line in the
  quickstart code block. `internal/server.New` reads `index.html`
  from the embed FS at server startup, substitutes the sentinel
  against the package-level `Version` variable via
  `bytes.ReplaceAll`, and serves the resulting bytes from a
  closure that wraps `http.ServeContent`. Since `ServeContent`
  handles Content-Type, Content-Length, Last-Modified, HEAD
  body-stripping, `If-Modified-Since`, and Range requests, the
  switch from `FileServerFS` to a custom handler costs nothing
  in HTTP semantics. The substitution happens exactly once per
  process, the byte slice is shared across requests, and each
  request instantiates its own `bytes.NewReader` for the seek
  state. A new assertion in
  `TestServer_Root_ServesEmbeddedLanding` pins `"v"+Version` to
  the served body and forbids any leftover `{{version}}` sentinel
  — any future refactor that accidentally falls back to serving
  `index.html` raw will break loudly.

### Changed

- **`go.mod` module declaration** is now `jetd.one/pagefault`
  (was `github.com/jet/pagefault`). The vanity path is expected
  to resolve to the hosted repository at
  `github.com/jetd1/pagefault` via a meta-refresh served from
  `jetd.one`; stand-up of that redirect is out of scope for this
  commit.

- **Every Go import statement rewritten** — 144 occurrences
  across 51 files under `cmd/pagefault/`, `internal/**`, and the
  `web/` package now carry the `jetd.one/pagefault/…` prefix.
  The rewrite was mechanical (`git ls-files '*.go' | xargs sed`)
  and verified by `go mod tidy` + `make build` + `make test
  -race` + `make lint` — every package still builds and every
  test still passes.

- **`CLAUDE.md` directory-tree** comment next to `go.mod` now
  records the vanity path and notes that the repo itself still
  lives at `github.com/jetd1/pagefault`, to defend against the
  same import-path / hosting-URL confusion that bit the landing
  site in 0.11.0.

### Not changed

- **GitHub repo URL.** Hyperlinks in `web/index.html`,
  `README.md`, the site footer, and every `gh` invocation
  continue to point at `https://github.com/jetd1/pagefault`.
  The Go module path and the repository hosting URL are two
  independent identifiers; changing one does not imply the
  other.

- **Binary surface.** Binary name, CLI verbs, YAML config keys,
  HTTP routes, REST / MCP / SSE / OAuth2 wire formats, and
  audit-log field names are all untouched.

- **Historical CHANGELOG entries.** The prior rename from
  `page-fault` to `pagefault` that set the module path to
  `github.com/jet/pagefault` is left as it was shipped — it
  correctly recorded the state at the time of that release and
  is not rewritten.

## 0.11.0 (2026-04-12)

First user-facing surface beyond the API. `pagefault serve` now
answers `GET /` with a proper HTML landing page — hero, concept,
tools table with inline glyph icons, quickstart, transports, and
an ASCII architecture diagram — instead of the plain-text endpoint
dump. Everything lives in the `web/` directory as pure HTML / CSS
/ JS / SVG with no build step and is embedded into the binary via
`//go:embed`, so deployment is still "drop one file on a box".

### Added

- **Design system (`docs/design.md`).** Eleven sections defining
  concept, voice, color tokens, typography, iconography, spacing,
  motion, accessibility, and error-state vocabulary for every
  future user-facing surface. The "fault / running / resolved"
  semantic palette maps directly onto the `task.Status` enum in
  `internal/task/task.go`, so HTML, CLI output, and HTTP error
  envelopes all speak the same color language. The `web/` landing
  site is the first surface built against the doc; any new UI —
  dashboard, admin panel, future interactive demo — should update
  the doc first or follow it.

- **Embedded landing site (`web/`).** New `web` Go package with
  `embed.go` exporting `Files embed.FS` plus five static assets
  (no build step, openable as `file://` in a browser too):
  - `index.html` — hero + concept + tools table + quickstart +
    transports + architecture + outro + footer
  - `styles.css` — CSS custom-property design tokens, components,
    responsive breakpoints, `prefers-reduced-motion` compliance,
    and a print stylesheet
  - `script.js` — hero terminal animation cycling a canonical
    `pf_fault` call through `fault → handler → resolved` using a
    cancellation-token state machine, with IntersectionObserver
    pause when offscreen and reduced-motion skip-to-end
  - `favicon.svg` — the pagefault logomark (rounded page with a
    diagonal "fault" slice at one corner and an inward load
    chevron), shipping both a full `#mark` symbol and a
    simplified `#mark-16` favicon preset
  - `icons.svg` — seven tool-glyph symbols (`#maps`, `#load`,
    `#scan`, `#peek`, `#fault`, `#ps`, `#poke`) drawn to design
    doc §5.2, inlined into the tools-table row leading columns
    via `<use href="./icons.svg#X">`

- **Static asset routes (`internal/server`).** Five explicit GET
  + HEAD pairs mount the embedded FS on the router: `/` serves
  `index.html`, and `/styles.css`, `/script.js`, `/favicon.svg`,
  `/icons.svg` each serve their own file, all through
  `http.FileServerFS(web.Files)` so content types come from the
  file extension and conditional requests / range handling come
  from the stdlib. Each path is registered explicitly rather than
  as a catch-all so it never shadows `/api/*`, `/mcp`, `/sse`, or
  the OAuth2 endpoints. HEAD is registered alongside GET because
  `chi.Get` does not imply HEAD the way `net/http.FileServer`
  does, and HEAD matters for link-preview crawlers (Slack,
  Discord, Twitter) and reverse-proxy health probes.

- **Test coverage.** New `TestServer_Root_ServesEmbeddedLanding`
  asserts the `text/html` content type, the `<!doctype html>`
  header, the brand / hero anchor, and every `pf_*` tool name on
  the page. New `TestServer_StaticAssets_Served` is table-driven
  across all four assets, checking status + content-type + a
  body substring that proves it's the right file (`--accent` in
  styles.css, `IntersectionObserver` in script.js,
  `<symbol id="mark"` in favicon.svg, `<symbol id="fault"` in
  icons.svg).

### Changed

- **`GET /` serves HTML instead of plain text.** The 30-line
  plain-text `handleRoot` method in `internal/server/server.go`
  that emitted a hand-formatted endpoint list has been deleted
  and replaced with the static file server. Content type flips
  from `text/plain; charset=utf-8` to `text/html; charset=utf-8`;
  response body is now the landing site.

- **Obsolete landing-text test deleted.**
  `TestServer_SSE_Disabled_RootLandingHidesIt` is gone. Its
  premise — that `/` only mentions `/sse` when SSE is actually
  mounted — applied to the old plain-text handler that
  dynamically enumerated the live route set, and does not apply
  to a static marketing HTML that describes the product. The
  underlying behavioural property ("SSE disabled → `GET /sse`
  returns 404") is already covered by
  `TestServer_SSE_DisabledReturns404` immediately above it, so
  no coverage is lost.

### Internal

- `CLAUDE.md` directory tree now lists `docs/design.md`, the
  `web/` package, `web/embed.go`, and every asset file under it.
  Any new surface added under `web/` (or any new design-governed
  file added elsewhere) should append itself to both `CLAUDE.md`
  and the "Surfaces" table in `docs/design.md` §11.

## 0.10.1 (2026-04-11)

Retrospective-driven fixes for three regressions introduced with the
0.10.0 async task manager. The behavioural gaps were not caught by
the 0.10.0 test suite because no tests exercised backend panics or
non-timeout subagent failures in the detached-goroutine flow.

### Fixed

- **Panic in subagent `Spawn` no longer crashes the server.** Before
  0.10.0, `target.Spawn` was called synchronously from the HTTP
  request handler goroutine, so `net/http`'s built-in panic recovery
  absorbed any panic as a 500. 0.10.0 moved `Spawn` onto a detached
  task-manager goroutine where `net/http` cannot reach it; an
  unrecovered panic there crashed the entire `pagefault` binary. The
  task manager now wraps every `req.Run` call in `defer recover()` so
  panics are converted into `StatusFailed` tasks with
  `Error="task: subagent panic: <value>"`, and the running counter is
  released correctly. A new `TestSubmit_PanicRecovered` regression
  test under `-race` exercises the path.

- **`pf_poke` mode:agent reports failed subagent writes as failures,
  not as `written:success`.** The 0.10.0 dispatcher encodes backend
  errors on the task snapshot (`Status=failed`, `Error=...`) rather
  than returning them via the Go error channel, and the pf_poke
  handler was hardcoding `Status:"written"` regardless — so a failed
  subagent silently returned `{status:"written", result:""}` and the
  content was lost. `handleWriteAgent` now switches on `res.Status`
  and returns `ErrBackendUnavailable` for `failed` / non-terminal
  snapshots. Two new regression tests cover the failure and
  caller-cancel-mid-wait paths.

- **`pf_poke` mode:agent surfaces caller-cancel mid-Wait as an
  error.** If the caller's HTTP context cancelled while the subagent
  was still running, the dispatcher returned the running snapshot
  with `err=nil` (intentional, for pf_fault polling) and pf_poke
  then reported the write as successful. Same switch statement
  rejects non-terminal snapshots from the sync write path.

- **`task.Manager.Wait` is immune to the TTL sweep race.** The
  done-channel path used to call `Get(id)`, which runs a sweep and
  could reclaim the just-finished entry between the done signal
  and the map lookup under a sub-second TTL. `Wait` now snapshots
  `e.task` directly under lock, bypassing the sweep entirely. The
  returned entry pointer keeps the record alive through
  concurrent reclaims.

- **`dispatcher.New` error path no longer leaks the task manager.**
  `serve.go`'s `buildDispatcher` now calls `_ = tasks.Close()`
  alongside `_ = auditLog.Close()` when `dispatcher.New` fails.
  Harmless in practice (the freshly-constructed manager owns no
  goroutines yet), but symmetric with the audit logger cleanup.

- **Deep-retrieve audit entries capture the wrapped spawn-id error.**
  `GenerateSpawnID` failure now assigns the wrapped
  `"dispatcher: generate spawn id: ..."` context to the audit-log
  local before returning, so audit rows carry the dispatcher frame
  instead of just the raw `rand.Read` failure. Minor hygiene.

- **`slog.Warn` on subagent timeout drops the placeholder empty
  `task_id` field.** The 0.10.0 closure could not access the
  dispatcher-assigned task id (the variable is declared by the
  `Submit` statement the closure is an argument to), so the log
  hint was hardcoded to `""`. Removed — the `spawn_id` is enough
  for correlation, and a task always has exactly one.

## 0.10.0 (2026-04-11)

Two architectural changes motivated by real-world openclaw integration:
pf_fault is now async-by-default, and subagent backends can mint a
fresh per-call session token via a new `{spawn_id}` placeholder.
Together they fix the "every pf_fault pollutes the main session"
class of bugs — previously `openclaw agent run` with `--session-id`
from the command template still keyed to `agent:main:main` because
the session key is derived from agent id, and every retrieval ran
against the same shared session.

### Added

- **`{spawn_id}` placeholder for subagent backends.** `subagent-cli`
  and `subagent-http` accept a new `{spawn_id}` token anywhere in
  their command / URL / body template. The dispatcher mints a
  cryptographically random `pf_sp_*` token per call and substitutes
  it in-place, so operators who wire it into their agent runner's
  session flag (e.g. `openclaw agent run --session-id {spawn_id}`)
  get one fresh session per `pf_fault` call and no cross-call
  context bleed. Backwards compatible — the substitution is a
  silent no-op for command templates that do not reference
  `{spawn_id}`. The spawn id is also surfaced in the tool response
  and audit entry so operators can correlate a pagefault call to
  a downstream session log. See `configs/example.yaml` for the
  canonical openclaw wiring.

- **In-memory async task manager (`internal/task`).** Every
  `pf_fault` / `pf_poke` mode:agent call flows through a new task
  manager that runs the subagent on a **detached goroutine** — the
  caller's HTTP request can disconnect, the proxy can time out, and
  the subagent still runs to completion. State lives in a
  sync-mutex-guarded map keyed on `pf_tk_*` task ids; terminal
  tasks are reclaimed by a best-effort sweep after `ttl_seconds`
  (default 600s / 10 minutes). `max_concurrent` (default 16) caps
  the number of in-flight goroutines; submissions past the cap
  return `rate_limited` so the HTTP response is a clean 429 instead
  of an opaque queue. Graceful shutdown cancels every in-flight
  task's context and waits for the goroutines to exit. Unit tests
  under `-race` exercise happy-path / failure / timeout / detached
  context / max-concurrent / sweep / concurrent-submit / close.

- **`server.tasks` config block.** Two fields — `ttl_seconds` and
  `max_concurrent` — both with sensible defaults. Zero-value
  config is fine for most deployments; tune `max_concurrent`
  higher for operators running many concurrent agents.

### Changed

- **`pf_fault` is async by default.** The call now submits the
  work to the task manager and returns immediately with
  `{task_id, status: "running", agent, backend, spawn_id}`.
  Callers **must poll** `pf_ps(task_id=...)` every ~30 seconds
  (up to 6 times for the default 120s budget — the canonical
  "30s × 6" pattern) until the status is terminal (`done`,
  `failed`, `timed_out`). The MCP tool description for `pf_fault`
  spells out the polling guide so calling agents see it
  alongside the argument schemas.

  **Backwards-compat escape hatch:** set `wait: true` on the
  `pf_fault` input to restore the old synchronous behaviour —
  the handler blocks until the task is terminal and returns the
  full answer inline. Sync mode is bounded by `timeout_seconds`
  and is vulnerable to HTTP-level middleware timeouts, so it is
  recommended only for CLI scripts and tests; MCP agent clients
  should stick with the async + poll default. The CLI (`pagefault
  fault`) still defaults to sync for human-friendly blocking
  behaviour, with a new `--async` flag for testing the poll path.

- **`pf_ps` extended with `task_id` polling mode.** Classic
  "list agents" behaviour when the request has no `task_id`
  (unchanged). When `task_id` is set, `pf_ps` returns the task
  snapshot (`{status, answer?, partial_result?, error?,
  elapsed_seconds, agent, backend, spawn_id}`) instead of the
  agent list. Unknown or TTL-expired ids return
  `resource_not_found` → HTTP 404. The tool description is
  rewritten to cover both modes. CLI form (`pagefault ps
  <task_id>`) picks the mode from presence of a positional arg.

- **`pf_poke` mode:agent stays synchronous by default.** Write
  calls are typically expected to confirm placement before
  returning, so `handleWriteAgent` passes `Wait: true` to
  `DelegateWrite`. Behind the scenes the spawn still runs on the
  task manager's detached goroutine, so a client HTTP disconnect
  no longer kills the write, but the caller still receives the
  full result inline. No wire change for `pf_poke` clients.

- **`ToolDispatcher.Close()` now also closes the task manager,**
  which cancels every in-flight task and waits for the goroutines
  to return. `buildDispatcher` wraps the close order so shutdown
  is: cancel tasks → wait on goroutines → flush audit sink.

## 0.9.1 (2026-04-11)

Follow-up to 0.9.0. A review pass against the DCR commit turned up
three doc-drift items and two minor code polish nits. No behavior
change beyond the constant-time compare; existing tests keep passing
unmodified.

### Changed

- **`handleOAuthRegister` DCR bearer-token check uses
  `subtle.ConstantTimeCompare`.** The 0.9.0 gate compared the
  submitted token against the configured `dcr_bearer_token` with a
  plain `!=` string compare. Timing-side-channel risk is low in
  practice (the token is operator-set, opaque, and the attack
  surface is a reverse-proxied HTTP endpoint where network jitter
  dominates), but bcrypt for client secrets and
  `crypto/subtle.ConstantTimeCompare` for PKCE challenges are used
  elsewhere — this makes the DCR path consistent with the rest of
  the OAuth2 code.
- **`isLocalhostOrHTTPS` comment rewritten.** The inline comment on
  the loopback branch read "localhost must still be http(s)", which
  was confusing — https://localhost is already accepted by the
  preceding `u.Scheme == "https"` case, so the loopback branch only
  needs to additionally allow plain `http`. The new comment spells
  that out. Logic unchanged; the existing `TestIsLocalhostOrHTTPS`
  table still pins every supported and rejected URI shape.

### Docs

- **`README.md` — Recent Changes reflects 0.9.0.** The section was
  missing the 0.9.0 entry entirely and still showed 0.7.1 at the
  bottom. It now holds 0.9.1 / 0.9.0 / 0.8.1 per the CLAUDE.md
  "three most recent" rule.
- **`README.md` — intro paragraph mentions 0.9.0's DCR.** The
  narrative used to stop at 0.8.0's PKCE flow as the headline
  OAuth2 feature; it now frames 0.9.0's RFC 7591 DCR as the layer
  on top that lets Claude Desktop's remote-connector UI
  self-register a public client against `POST /register` without a
  manual `pagefault oauth-client create` step.
- **`docs/security.md` — rate-limiter bullet now covers `/register`.**
  The "Public OAuth2 endpoints are outside the in-process rate
  limiter" note previously said "Of the four" and only discussed
  `/oauth/authorize`'s per-request memory cost. With DCR there is
  a fifth endpoint (`/register`) that *also* carries a per-request
  cost — each successful call does a JSONL append + fsync and adds
  one `ClientRecord` to the in-memory map, and unlike
  authorization codes these records are persistent (no TTL sweep),
  so sustained registration traffic grows the file and the map
  without bound. The bullet now splits into a sub-list covering
  both `/oauth/authorize` and `/register`, and the proxy-level
  rate-limit recommendation now lists `/register` alongside the
  other four when DCR is enabled.

## 0.9.0 (2026-04-11)

### Added

- **RFC 7591 Dynamic Client Registration (`POST /register`).**
  When `auth.oauth2.dcr_enabled: true` is set, pagefault mounts a public
  `/register` endpoint that allows MCP clients like Claude Desktop to
  self-register as public OAuth2 clients (PKCE-only, no client_secret)
  without manual `pagefault oauth-client create`. The endpoint is
  advertised in the RFC 8414 authorization server metadata as
  `registration_endpoint`. DCR is opt-in because it creates client
  records without authentication; operators who want to gate registration
  can set `auth.oauth2.dcr_bearer_token` to require a bearer token on
  the registration request. Redirect URIs are restricted to localhost or
  HTTPS per MCP security conventions. Dynamically-registered clients
  get a `pf_dcr_` prefix on their client ID and persist across restarts
  via append-only JSONL writes. The `oauth-client ls` output now shows
  a SOURCE column (`cli` vs `dcr`) to distinguish registration method.
  `grant_types: ["refresh_token"]` is silently accepted (pagefault does
  not issue refresh tokens) to avoid breaking Claude Desktop's DCR
  request.

### Changed

- **CORS middleware always short-circuits preflights with 204.**
  Previously, preflight requests from disallowed origins fell through
  to downstream handlers, producing confusing 401/405 responses.
  Now all preflights return 204 — allowed origins get CORS headers,
  disallowed origins get no CORS headers (the browser rejects either
  way). This prevents the auth middleware from rejecting CORS preflight
  OPTIONS requests before the CORS middleware can handle them.

- **Auth middleware passes CORS preflights through.** The auth
  middleware now skips authentication for OPTIONS requests with an
  `Access-Control-Request-Method` header, letting the CORS middleware
  handle them. Browsers never attach credentials to preflight requests,
  so authenticating them always fails and blocks the subsequent real
  request.

## 0.8.1 (2026-04-11)

### Security

- **Open redirect on `/oauth/authorize` (pre-registration error path).**
  `handleOAuthAuthorize` validated `response_type` before it validated
  `client_id` and the registered `redirect_uri`. An attacker could
  send `response_type=bogus&redirect_uri=https://evil.example.com/&state=…`
  and the handler would 302 to the unregistered URI with the error
  envelope — a textbook open redirect on a publicly advertised
  discovery endpoint. RFC 6749 §4.1.2.1 explicitly forbids this.
  Validation is now strictly ordered: `client_id` → client lookup →
  `redirect_uri` presence → registered-URI match, and only after the
  redirect target is confirmed safe may any subsequent error trigger
  an `authorizeError` redirect. A client with zero registered URIs is
  now rejected up front (it was created for `client_credentials` and
  has no business on `/oauth/authorize`).

- **Consent form `action` injection bypass.** When `auto_approve: false`,
  `renderConsentPage` echoed every query parameter back as a hidden
  form field. An attacker URL carrying `&action=allow` landed a
  hidden `<input name="action" value="allow">` ahead of the submit
  buttons; Go's `url.Values.Get("action")` returns the first value,
  so the user's "Deny" click was silently overridden by the injected
  value. Two-layer fix: (a) the hidden-field renderer is now a strict
  whitelist of OAuth spec parameters (`response_type`, `client_id`,
  `redirect_uri`, `scope`, `state`, `code_challenge`,
  `code_challenge_method`); (b) the POST handler takes the **last**
  `action` value and requires it to be literally `"allow"` — anything
  else (missing, empty, unknown, attacker-seeded) is treated as deny.

- **Clickjacking defence on the consent page.** The CSP on the
  consent form now includes `frame-ancestors 'none'` alongside
  `default-src 'none'`, `style-src 'unsafe-inline'`, and
  `form-action 'self'`. Low blast radius (auto_approve is on by
  default, and pagefault has no user session to hijack), but a
  one-line defence-in-depth change.

### Changed

- **`invalid_grant` / `invalid_client` classification uses
  `errors.Is`.** The token endpoint's authorization-code error
  branch was classifying errors by `err.Error() == "oauth2: invalid
  grant"` / `strings.Contains`. It now calls
  `errors.Is(err, auth.ErrInvalidGrant)` against the exported
  sentinels, so the classification survives message tweaks and
  future error wrapping. The client-facing `error_description` is
  now a static sanitized string instead of `err.Error()`, so no
  internal detail leaks on the error path.

- **`IssueAuthorizationCode` library-level guard.** The provider
  method used to fall through silently when the client had zero
  registered `redirect_uris` **and** the caller passed an empty
  `redirectURI` argument. The HTTP handler already blocks that
  combination, but `IssueAuthorizationCode` is exported and could
  be called by library consumers. Now rejects `len(rec.RedirectURIs)
  == 0` up front with `ErrInvalidRequest` regardless of the
  supplied `redirectURI`, so the defensive guarantee matches
  whether the caller comes through HTTP or Go code. A second
  assertion in `TestOAuth2Provider_IssueAuthorizationCode_NoRegisteredURIs`
  pins the empty-arg case.

- **`expires_in` clamped to ≥ 1.** `writeTokenResponse` used to
  compute `int(time.Until(ExpiresAt).Seconds())`, which could
  return 0 for sub-second TTLs or negative under clock skew.
  RFC 6749 §5.1 requires a positive integer, and clients that key
  off the value as "seconds until refresh" misbehave on zero or
  negative. The clamp logic is now in `computeExpiresIn(expiresAt,
  now)` — a package-private helper that rounds up to the next
  whole second and clamps to 1 — with table-driven unit tests
  covering typical-TTL / sub-second / exactly-expired / already-
  expired / latency-spike / fractional-second cases.

### Testing

- **Regression tests for all three security fixes.**
  `TestOAuth2_Authorize_NoOpenRedirect_UnregisteredURI` and
  `…_MissingClientID` pin the RFC 6749 §4.1.2.1 ordering.
  `TestOAuth2_Authorize_ConsentPage_ParamInjectionBypass` drives the
  attacker POST body with two `action` values and asserts the last
  one (the user's click) decides the outcome.
  `TestOAuth2_Authorize_ConsentPage_WhitelistDropsInjectedAction`
  checks the rendered HTML and the `frame-ancestors` CSP in one go.
  `TestOAuth2_Authorize_ConsentPage_DefaultDeny` /
  `…_Allow` pin the POST state machine.

- **CLI coverage for `--public` and `--redirect-uris`.** The 0.8.0
  CLI flags previously had zero direct tests.
  `TestOAuthClientCreate_Public`,
  `…_PublicRequiresRedirectURIs`,
  `…_ConfidentialWithRedirectURIs`, and
  `TestRunOAuthClientList_ShowsTypeAndRedirectURIs` now cover the
  happy paths, the guard rail, the mixed-mode case, and the `ls`
  output format.

### Docs

- **`plan.md` removed.** The file was a 1033-line mix of
  architectural spec, phase roadmap, deployment-specific
  (OpenClaw/Hermes) context, and open questions. Most of its
  content had either shipped and been superseded by `CHANGELOG.md`,
  or drifted out of date (OAuth2 was still listed as "Phase 5 /
  future" despite shipping in 0.7.0 and 0.8.0). Load-bearing
  architectural content migrated into the existing docs:
  - `docs/architecture.md` — added full **OAuth2 wiring** section
    (provider construction, public endpoint mounts, compound mode
    fallback), expanded the auth-layer list from three to four
    providers, added OAuth2 to the component diagram, added a
    Shipped-milestones / Not-yet-shipped split to replace the
    plan.md §10 phase roadmap.
  - `docs/security.md` — added **Design intent: thin auth, reverse
    proxy expected** section at the top (pagefault does not ship
    TLS; `trusted_header` is first-class). Expanded the OAuth2
    authorize-endpoint section to document the 0.8.1 validation
    order explicitly (eight-step pipeline), the `consentParams`
    whitelist, the take-last-action / default-deny semantics on the
    consent POST, the `frame-ancestors 'none'` CSP, and the
    `errors.Is` classification. Added **"Public OAuth2 endpoints
    are outside the in-process rate limiter"** bullet covering the
    ~18 MB authorization-code memory bound under abuse and the
    recommended proxy-level rate-limit configuration, with a
    cross-reference from the "Rate limiting is in-process only"
    known-limitation note. Added the **"why agent writes bypass
    `write_paths` on purpose"** rationale to the mode:agent
    trust-boundary discussion.
  - `docs/config-doc.md` — collapsed the split `GET /oauth/authorize`
    / `POST /oauth/authorize` rows into a single `GET+POST
    /oauth/authorize` entry so the stated endpoint count matches
    `docs/api-doc.md` and `docs/architecture.md` ("four public
    endpoints").
  - `docs/api-doc.md` — expanded the OAuth2 section to cover both
    grant types and all four public endpoints
    (client_credentials + authorization_code + PKCE walkthrough).
    Bumped the health-endpoint example version to 0.8.1 and noted
    that the `version` field drifts with the VERSION file. Dropped
    the "OAuth2 is Phase 5 future work" framing from the Planned
    section.
  - `README.md` — rewrote the intro paragraph to feature 0.8.0's
    authorization_code + PKCE flow as the primary OAuth2
    experience (0.7.0 client_credentials is now framed as the
    programmatic fallback). Dropped the plan.md documentation
    bullet and added a CHANGELOG.md pointer in its place.
  - `CLAUDE.md` — updated the directory tree to drop the plan.md
    entry, refreshed the `oauth_client.go` /
    `auth/oauth2.go` / `server/oauth2.go` / `server/oauth2_test.go`
    descriptions to cover the 0.8.0 and 0.8.1 additions, updated
    the "Running Locally" OAuth2 examples to show both
    confidential and `--public --redirect-uris` client creation,
    expanded the Auth abstraction bullet from three to four
    public endpoints, removed the plan.md entry from the See Also
    section.

  Historical CHANGELOG references to plan.md (in the 0.4 / 0.5 /
  0.6 / 0.7 entries) are intentionally preserved — they describe
  what was true at the time each version shipped and rewriting
  them would be revisionist.

## 0.8.0 (2026-04-11)

### Added

- **OAuth2 authorization code + PKCE flow.** The MCP specification
  requires OAuth 2.1 with PKCE for browser-based clients like Claude
  Desktop. pagefault now supports the full authorization_code grant
  alongside the existing client_credentials grant. New public
  endpoints:

  - `GET /oauth/authorize` — the authorization endpoint; validates
    `response_type=code`, `client_id`, `redirect_uri`, `state`,
    `code_challenge`, `code_challenge_method=S256`, then either
    auto-approves (redirects immediately with the code) or renders a
    consent page (configurable, default auto-approve).
  - `POST /oauth/authorize` — consent form handler (when
    auto_approve is false).
  - `POST /oauth/token` extended — now also accepts
    `grant_type=authorization_code` with `code`, `redirect_uri`,
    `client_id`, and `code_verifier` (PKCE).

- **PKCE with S256.** The only `code_challenge_method` supported is
  `S256` (`BASE64URL(SHA256(code_verifier))`), as required by OAuth
  2.1. The `plain` method is rejected. PKCE verification uses
  constant-time comparison (`crypto/subtle.ConstantTimeCompare`).

- **Public clients.** ClientRecords with an empty `secret_hash` and
  at least one `redirect_uri` are treated as public clients. They
  authenticate via PKCE alone — no `client_secret` is needed at the
  token endpoint. This is the pattern Claude Desktop uses.

- **Updated RFC 8414 metadata.** The
  `/.well-known/oauth-authorization-server` response now includes
  `authorization_endpoint`, `code_challenge_methods_supported`,
  `grant_types_supported: ["client_credentials", "authorization_code"]`,
  `response_types_supported: ["code"]`, and
  `token_endpoint_auth_methods_supported: ["client_secret_basic",
  "client_secret_post", "none"]`.

- **`--redirect-uris` flag on `oauth-client create`.** Comma-separated
  list of allowed redirect URIs for the authorization_code flow.
  Required for any client that will use the auth code flow.

- **`--public` flag on `oauth-client create`.** Creates a public client
  (no `client_secret`, PKCE-only). Requires `--redirect-uris`.

- **`oauth-client ls` now shows client type and redirect URIs.** The
  table includes a TYPE column (confidential/public) and a
  REDIRECT_URIS column.

- **New config fields.** `auth.oauth2.auth_code_ttl_seconds` (default
  60) controls authorization code lifetime. `auth.oauth2.auto_approve`
  (default true) controls whether the authorize endpoint skips the
  consent page.

- **`OAuth2Provider.ValidateClientSecret` method.** Validates a
  client_secret against the stored bcrypt hash without issuing a token.
  Used by the authorization_code token exchange for confidential
  clients.

- **`OAuth2Provider.LookupClient` method.** Returns a ClientRecord by
  client ID. Used by the authorize endpoint to validate client_id and
  look up registered redirect URIs.

- **`OAuth2Provider.IssueAuthorizationCode` method.** Issues a
  short-lived, one-time-use authorization code bound to a client,
  redirect_uri, and PKCE code_challenge.

- **`OAuth2Provider.ExchangeAuthorizationCode` method.** Validates and
  consumes an authorization code, verifies PKCE, and issues an access
  token.

### Changed

- **`ReloadClients` now allows public clients.** ClientRecords with an
  empty `secret_hash` are accepted when they have at least one
  `redirect_uri`. Previously any record with an empty `secret_hash`
  was rejected.

- **`ClientRecord` has a new `RedirectURIs` field.** Additive change —
  existing JSONL files without this field parse correctly with an
  empty slice.

- **`sweepExpiredLocked` also sweeps auth codes.** Expired
  authorization codes are cleaned up alongside expired access tokens.

### Notes

- No dynamic client registration (DCR) endpoint is provided. Operators
  pre-register clients via `pagefault oauth-client create` and paste
  credentials into the MCP client's configuration UI. This is a
  deliberate security decision for internet-facing deployments — an
  open registration endpoint would let anyone create a client.

- The consent page (when `auto_approve: false`) renders minimal HTML
  with CSP headers. The default is `auto_approve: true` because on a
  single-operator self-hosted server the operator is authorizing
  themselves.

## 0.7.1 (2026-04-11)

OAuth2 review-pass hardening. External reviewer flagged three
issues in the 0.7.0 implementation; this release fixes the two
actionable ones and documents the third.

### Added
- **`OAuth2Provider.RevokeClient(clientID) int`**
  (`internal/auth/oauth2.go`). Removes the in-memory client record
  and purges every access token currently issued to that client,
  returning the number of tokens purged. Exists so a future
  in-process hook (SIGHUP reload handler or an authenticated admin
  endpoint) can force immediate invalidation without waiting for
  the access_token TTL. The CLI still runs out-of-process today, so
  the `pagefault oauth-client revoke` command cannot yet call this
  directly — the CLI revoke message has been rewritten to make
  that gap obvious, and a Phase-5 TODO is pinned next to the code.
- **`TestOAuth2Provider_RevokeClient`** and
  **`TestOAuth2Provider_ReloadClients_SweepsRevokedTokens`**
  (`internal/auth/oauth2_test.go`) covering the two sweep paths.
- **`TestOAuth2_Token_GrantTypeInQueryRejected`**
  (`internal/server/oauth2_test.go`) pinning the strict-body
  behaviour below.

### Changed
- **`OAuth2Provider.ReloadClients` now sweeps orphaned tokens.**
  When a reload removes a client from the in-memory map, every
  issued access token for that client is deleted as part of the
  reload. This makes file-based revocation (rewrite JSONL →
  reload) fully invalidate active sessions in one step, subject
  to the operator actually triggering the reload. Tokens for
  still-present clients are untouched, so reloads that add or
  edit unrelated records do not sign users out.
- **Token endpoint is now strict about `grant_type` placement.**
  `POST /oauth/token` reads `grant_type` from `r.PostForm` only,
  not the URL query string. A client that passes
  `?grant_type=client_credentials` in the query now gets
  `unsupported_grant_type`, matching RFC 6749 §4.4's requirement
  that the field arrive in the application/x-www-form-urlencoded
  body. The previous lenient fallback was removed so the bug is
  visible in the client's logs at integration time rather than
  silently succeeding.
- **`pagefault oauth-client revoke` output rewritten** to spell
  out the in-process gap: access tokens issued to the revoked
  client remain valid until (a) the access_token TTL expires or
  (b) pagefault is restarted, because the CLI writes the clients
  file out-of-process and cannot reach the running server's
  in-memory token store. A Phase-5 TODO is pinned in the code
  referencing the future SIGHUP reload / admin endpoint.

### Notes on reviewer findings not acted on in 0.7.1
- **`extractClientCredentials` precedence.** Reviewer confirmed
  the existing Basic-takes-precedence-over-form-body behaviour
  matches RFC 6749 §2.3. No change.
- **`sweepExpiredLocked` cadence.** Still opportunistic (on each
  new issue). A background ticker adds goroutine lifecycle
  complexity for a benefit that only matters at high token
  volumes; deferred.
- **`resolveIssuer` per-request recomputation.** String concat is
  truly negligible; not worth a cached field.
- **`configs/production.yaml` still on bearer mode.** Operational
  migration question, not a code issue.

## 0.7.0 (2026-04-11)

OAuth2 client_credentials auth provider. Shipped to unblock
Claude Desktop's built-in SSE MCP configuration, which as of 2026-04
only accepts **Client ID / Client Secret** credentials in its UI —
there is no field for attaching a plain `Authorization: Bearer pf_...`
header to the SSE GET. Before this release, a bearer-auth
pagefault deployment could only serve Claude Desktop via the
`npx supergateway` bridge; with 0.7.0, operators can register a
Claude Desktop client with `pagefault oauth-client create` and
point Claude Desktop directly at the pagefault URL.

### Added
- **`auth.mode: "oauth2"` provider** (`internal/auth/oauth2.go`).
  Implements RFC 6749 §4.4 client_credentials grant against an
  operator-managed client registry. Opaque access tokens with
  configurable TTL (default 3600s) are held in an in-memory store;
  expired entries are swept lazily on lookup and opportunistically
  on issue. Tokens are scoped by intersection of the client's
  allowed scopes and the caller-requested scopes; the default scope
  set is `["mcp"]` to match the MCP client-ecosystem convention.
- **Compound mode.** When `auth.mode: "oauth2"` is configured
  alongside a populated `auth.bearer.tokens_file`, long-lived bearer
  tokens from the JSONL store continue to authenticate as a
  fallback. This lets operators migrate Claude Desktop to OAuth2
  without breaking existing Claude Code deployments that still rely
  on static bearer tokens. The fallback is constructed lazily in
  `NewOAuth2Provider` and reuses the existing `BearerTokenAuth`
  implementation, so audit entries and caller metadata are identical
  regardless of which validator matched.
- **Three public HTTP endpoints** (`internal/server/oauth2.go`),
  mounted outside the auth middleware because they must work before
  any token exists:
  - `GET /.well-known/oauth-protected-resource` (RFC 9728 metadata,
    points `authorization_servers` at the pagefault issuer)
  - `GET /.well-known/oauth-authorization-server` (RFC 8414 metadata
    advertising `grant_types_supported: ["client_credentials"]` and
    `token_endpoint_auth_methods_supported: ["client_secret_basic",
    "client_secret_post"]`)
  - `POST /oauth/token` (the client_credentials grant endpoint,
    accepts both HTTP Basic and form-body credentials; returns a
    standard RFC 6749 token response with `Cache-Control: no-store`)
- **`pagefault oauth-client` CLI subcommand** mirroring
  `pagefault token` — `create` / `ls` / `revoke` against an
  operator-managed JSONL clients file. `create` prints the Client ID
  and Client Secret exactly once; the secret is stored as a bcrypt
  hash and is never recoverable from the file afterwards. Supports
  `--scopes "mcp mcp.read"` to narrow a client's allowed scope set,
  `--id` for a custom id, and the standard
  `--config`/`--clients-file` resolution pair.
- **`auth.oauth2` config block** (`internal/config/config.go`):
  `clients_file` (required), `issuer` (optional override for the
  discovery documents), `access_token_ttl_seconds` (default 3600),
  `default_scopes` (default `["mcp"]`). `AccessTokenTTLOrDefault` /
  `DefaultScopesOrDefault` helpers return the resolved values for
  consumers that need the effective settings.
- **Issuer resolution.** The discovery endpoint handlers prefer the
  explicit `auth.oauth2.issuer` override, then fall back to
  `server.public_url`, and finally infer from the incoming request's
  scheme + host (honouring `X-Forwarded-Proto` / `X-Forwarded-Host`
  when present). Deployments behind a reverse proxy that rewrites
  Host without forwarding the original should pin one of the first
  two.
- **New CHANGELOG entries in the README** (the three most recent)
  and a new `docs/config-doc.md` section covering the full
  `auth.mode: oauth2` wiring with a worked Claude Desktop example.

### Changed
- `internal/auth/auth.go` — `NewProvider` gained an `oauth2` case
  that calls through to `NewOAuth2Provider`. The existing `none` /
  `bearer` / `trusted_header` branches are unchanged; operators
  flipping `auth.mode` to `oauth2` and adding a `clients_file`
  entry is the full upgrade path.
- `internal/config/config.go` — `AuthConfig.Mode` oneof validator
  now accepts `oauth2`. The new `OAuth2Config` struct is embedded
  on `AuthConfig`; empty is fine except when mode is oauth2.

### Fixed
- **`docs/security.md` drift from 0.6.0 wave G.** An earlier edit
  claimed "Claude Desktop sends the Authorization header on both
  the initial SSE GET and subsequent message POSTs, so bearer auth
  works end-to-end on the SSE transport without any special
  accommodation." This is incorrect — Claude Desktop's SSE config
  does not attach bearer headers at all. Rewritten to call out the
  OAuth2-only credential field and point at the new 0.7.0 path.

### Dependencies
- `golang.org/x/crypto/bcrypt` added for client secret hashing.
  Pulled in at module level via `go get`, pinning
  `golang.org/x/crypto v0.50.0` (up from `v0.49.0`).

### Tests
- `internal/auth/oauth2_test.go` — 13 tests covering
  IssueToken happy/invalid/unknown/empty, Authenticate with
  issued/expired/unknown/missing-header, compound-mode fallback,
  scope intersection, ReloadClients mid-run, missing-clients-file
  rejection, duplicate-id rejection, JSONL parser (comments +
  blanks), access token + client secret generator entropy.
- `internal/server/oauth2_test.go` — 11 integration tests via
  httptest covering discovery endpoint shapes, token endpoint
  Basic/post/invalid/unsupported-grant/missing-creds paths,
  end-to-end token → /api/pf_maps, compound-mode legacy bearer,
  unmounted-when-disabled, and RFC 6749 §2.3.1 URL-encoded Basic
  credential decoding.
- `cmd/pagefault/oauth_client_test.go` — full CLI lifecycle
  (create/ls/revoke, duplicate-id error, empty list, with-records
  list, stored hash verifies the printed secret, required-label
  error, unknown/missing subcommand errors, resolveClientsFile
  config-vs-flag precedence).

## 0.6.1 (2026-04-11)

Hotfix for a long-running `pf_fault` failure mode reported in a real
Claude Desktop deployment: tool calls died after "几十秒" (a few tens
of seconds) regardless of the caller-supplied `timeout_seconds`, well
before the subagent could finish.

Full trace: pagefault's internal code respects `timeout_seconds`
end-to-end (traced every `context.WithTimeout` from the HTTP request
through the dispatcher into `SubagentBackend.Spawn` — no hidden
clamp). The premature termination came from **idle connections being
killed by intermediate proxies during the subagent wait**. On the
native `/sse` transport, pagefault's current SSE server did not
enable mcp-go's keepalive feature, so the persistent GET /sse stream
sat silent for the full duration of the tool call and whichever
proxy timeout fired first (nginx `proxy_read_timeout` default 60s,
Node undici `headersTimeout` default 60s, Cloudflare free plan 100s,
…) closed the connection before the response arrived.

### Fixed
- **SSE keepalive pings enabled by default.** `internal/server`
  now passes `mcpserver.WithKeepAlive(true)` +
  `WithKeepAliveInterval(...)` when mounting `NewSSEServer`, so the
  persistent GET `/sse` stream emits a JSON-RPC `ping` event on a
  ticker (default every 15 seconds) for the whole lifetime of the
  connection. This keeps intermediate proxies from considering the
  connection idle and closing it, so a 60-120s `pf_fault` call
  completes cleanly instead of being killed mid-wait. The pings
  themselves are harmless — mcp-go's client side treats them as
  no-ops.
- Operators using `supergateway --streamableHttp → /mcp` are
  **not** covered by this fix. mcp-go's streamable-http transport
  only supports keepalives on the separate long-polling GET
  connection used for server-to-client notifications, not on the
  tool-call POST response.
- **Claude Desktop caveat.** As of 2026-04, Claude Desktop's
  built-in SSE MCP config only accepts OAuth2 Client ID / Client
  Secret as credentials — it does not expose a way to attach a
  plain `Authorization: Bearer pf_...` header to the initial SSE
  GET, so operators who authenticate with pagefault's bearer
  tokens cannot drop `supergateway` and point Claude Desktop at
  `/sse` natively. For that specific combination, the
  recommended path until pagefault ships an OAuth2 auth provider
  (tracked for Phase 5) is to keep the `supergateway` bridge and
  work around the proxy-timeout problem at the proxy layer
  instead — raise `proxy_read_timeout` / equivalent on whatever
  sits in front of pagefault.jetd.one to 300s or more. Other
  SSE clients (and future Claude Desktop versions that accept
  an `Authorization` header on the SSE URL) benefit from the
  keepalive fix directly.

### Added
- **`server.mcp.sse_keepalive`** (bool, defaults to `true`) —
  opt-out toggle for the SSE keepalive pings. Set to `false` if
  you have an unusual SSE client that rejects unsolicited server
  pings, or if you have confirmed your proxy chain never closes
  idle SSE streams.
- **`server.mcp.sse_keepalive_interval_seconds`** (int, defaults
  to `15`) — tuner for the ping ticker. Pagefault's default is
  longer than mcp-go's own 10-second default because 15s is still
  comfortably under every common proxy idle timeout and keeps the
  wire traffic a touch lighter. Set lower if you have a
  particularly aggressive proxy (e.g. nginx with
  `proxy_read_timeout 10s`); leaving it at 15 is safe for
  everything I have tested against. Values at or below zero are
  clamped to the default.

### Tests
- `TestServer_SSE_KeepAliveEmitsPing` — spins up a test server
  with `SSEKeepAliveIntervalSeconds: 1`, opens `/sse`, reads past
  the endpoint event, and asserts a `"method":"ping"` event
  arrives on the stream within 5 reads. Regression guard: if a
  future refactor silently drops the `WithKeepAlive` option, this
  test fails before any real deployment sees the "几十秒就挂"
  failure again.
- `TestServer_SSE_KeepAliveDisabledSuppressesPing` — opts out
  via `SSEKeepAlive: false` and asserts no further SSE data
  lands on the stream after the endpoint event (the reader
  blocks until the test's 3-second context cancels). This is
  the belt-and-braces guard that the toggle actually turns the
  feature off.

### Docs
- `docs/config-doc.md` §`server.mcp` gains rows for the two new
  fields plus a "Why keepalives?" paragraph explaining the
  idle-proxy failure mode.
- `configs/example.yaml` shows the new fields commented out,
  with an inline note pointing at the proxy-timeout problem the
  defaults are fixing.
- `README.md` Recent Changes gets a 0.6.1 hotfix entry.

## 0.6.0 (2026-04-11)

Real-deployment feedback pass, three waves. Running pagefault behind
live clients surfaced a cluster of agent-facing friction points that
the 0.5.x wire shape and terse tool descriptions did not cover:

1. **Claude Desktop could not connect.** It speaks MCP over legacy
   SSE and pagefault's `/mcp` endpoint was streamable-http only,
   forcing users into an `npx supergateway` bridge.
2. **Cold agents did not know *when* to reach for `pf_*` tools.**
   Nothing in the MCP handshake told them; adoption was
   inconsistent across sessions.
3. **Subagents behaved like generic Q&A bots.** A live `pf_fault`
   run for "what did I note about oleander" returned a
   world-knowledge toxicity sheet, because nothing told the
   subagent its job was memory retrieval — so it answered from
   training data.
4. **Per-parameter tool descriptions were too terse.** Agents did
   not know how to construct a good query, URI, or content payload.
5. **`pf_fault` had no time-window hook.** "What did I do last
   week" needed a way to scope the subagent's search.
6. **Agent selection was invisible.** A multi-agent setup
   (`wocha` = work, `cha` = personal) surfaced in `pf_ps` with
   clear routing descriptions, but `pf_fault`'s `agent` parameter
   documented "leave empty to use the first configured agent" —
   so the calling agent silently defaulted to whichever was listed
   first and missed the richer source. A second trace run showed
   the calling agent skipping `pf_ps` entirely, spawning `wocha`
   by accident, and only realising afterwards that `cha` would
   have had richer daily-notes data.
7. **Timeout guidance was actively harmful.** The old
   `timeout_seconds` description suggested "lower to 30-60 for
   simple recalls", but real `pf_fault` runs in the trace took
   22-29s just to return their first token — a 30s deadline
   would truncate nearly every run.

All addressed here without breaking any existing wire shape.
The Spawn interface gains a `SpawnRequest` struct so future knobs
can land without churning every call site again.

### Added
- **Native MCP legacy-SSE transport.** `GET /sse` opens a persistent
  `text/event-stream`, emits an `endpoint` event carrying a
  sessionId, and streams JSON-RPC responses back as `message` events;
  `POST /message?sessionId=...` accepts the paired message POSTs and
  202-Accepts before dispatching through the same `MCPServer` the
  `/mcp` route uses. Both transports share the same tool set, auth
  chain, rate limiter, and instructions — the only difference is the
  wire framing. Claude Desktop now connects natively; the
  `supergateway` workaround is no longer needed.
  - New `internal/server` wiring: `sseSrv := mcpserver.NewSSEServer(
    mcpSrv, …)`, mounted inside the existing auth group so bearer
    tokens flow through on both the SSE GET and the message POST.
  - `server.public_url`, when set, feeds mcp-go's `WithBaseURL` so
    the `endpoint` event emits an absolute URL — safer behind
    reverse proxies where relative resolution could land a client
    on the wrong host. Empty `public_url` keeps the old
    root-relative behaviour.
- **Opt-out `server.mcp.sse_enabled` toggle** (default `true`).
  Operators who only serve streamable-http clients can set it to
  `false` to drop the `/sse` + `/message` routes and shrink the
  public surface. The `TestServer_SSE_DisabledReturns404` test
  guards the toggle so a future refactor cannot accidentally
  re-mount the routes.
- **Server-level MCP instructions** via
  `mcpserver.WithInstructions()`. Most MCP clients (Claude Desktop,
  Claude Code, etc.) surface this string in the agent's system
  prompt, which makes it the single most reliable lever for
  teaching agents when pagefault's tools are the right move. Ships
  as a prescriptive default in the new
  `internal/tool/instructions.go` — it lists the signal phrases
  that should trigger a `pf_scan` / `pf_peek` / `pf_poke` call,
  prescribes the `pf_ps` → `pf_fault` flow for multi-agent setups,
  and carries explicit "do NOT call these tools for world-knowledge
  questions or current-repo code" guardrails so an eager agent
  does not spam `pf_scan` for every message.
- **`server.mcp.instructions` config override.** Setting a non-empty
  string in the YAML replaces the built-in default verbatim, so an
  operator can layer on installation-specific guidance like
  "daily notes live under `memory://daily/`, project docs under
  `memory://projects/`" without editing source.
- **Server-side subagent prompt templates.** Subagent backends
  (`subagent-cli` and `subagent-http`) now wrap the raw caller
  content with a resolved prompt template *before* substituting
  into their command / body template. The precedence chain is:
  per-agent override on `AgentSpec` → per-backend default on
  `Subagent*BackendConfig` → built-in constant for the purpose.
  This is the fix for the "wocha returned a generic toxicity
  sheet" failure mode — the built-in retrieval template
  explicitly tells the agent "you are a memory-retrieval agent,
  search the user's memory sources, do not fall back to world
  knowledge".
  - New `internal/backend/prompt.go` with `SpawnRequest`,
    `SpawnPurpose`, `ResolvePromptTemplate`, `WrapTask`, and the
    two default templates (`DefaultRetrievePromptTemplate` /
    `DefaultWritePromptTemplate`).
  - Retrieval template enumerates the memory sources a subagent
    should try (MEMORY.md, managed directories, qmd, sqlite /
    lossless-lcm, etc.) and explicitly forbids inventing content
    or falling back to training data.
  - Write template (for `pf_poke` mode:agent) frames the agent as
    a placement specialist — read the existing layout, match the
    naming convention, extend an existing file when themes
    overlap, report the path(s) written.
  - Templates support `{task}`, `{time_range}`, `{target}`, and
    `{agent_id}` placeholders. Unknown placeholders pass through
    unchanged.
  - Per-agent overrides via `AgentSpec.retrieve_prompt_template` /
    `AgentSpec.write_prompt_template` — same backend can host a
    strict retrieval agent and a freer summarisation agent
    without separate backend entries.
- **`pf_fault` time range.** New `time_range_start` /
  `time_range_end` optional free-form string parameters on the
  MCP, REST, and CLI surfaces (CLI: `--after` / `--before` —
  deliberately not `--from`/`--to` because peek already uses
  those for line numbers). Pagefault does not parse the values;
  they pass through to the subagent via the prompt template's
  `{time_range}` placeholder, formatted as `{start} to {end}` /
  `from {start} onwards` / `up to {end}` depending on which
  fields are populated. The subagent interprets the string in
  its own context, so any human-readable form works
  (ISO 8601, "last Tuesday", "Q1 2026").
- **Dispatcher `DeepRetrieveOptions` and `DelegateWrite`.**
  Dispatcher gains a `DeepRetrieveOptions` struct for
  `DeepRetrieve` (currently just `TimeRange`) and a new
  `DelegateWrite(content, agentID, timeout, caller, opts)` entry
  point for `pf_poke` mode:agent. `HandleWrite.handleWriteAgent`
  no longer tunnels through `DeepRetrieve` — a direct write call
  means the subagent gets `SpawnPurposeWrite` and therefore the
  write-framed prompt template, which is the whole reason this
  split exists.

### Changed
- `ServerConfig` grows a `MCP MCPConfig` sub-struct with
  `SSEEnabled *bool` and `Instructions string`. The pointer lets us
  distinguish "unset, use the default (true)" from "explicitly
  false", mirroring the pattern already used in `ToolsConfig`.
- `/` landing page advertises `/sse` and `/message` — but only when
  `sse_enabled` is actually on, so the root output reflects the
  real routing table.
- **`backend.SubagentBackend.Spawn` signature.** Was
  `Spawn(ctx, agentID, task, timeout)`; is now
  `Spawn(ctx, SpawnRequest)`. The struct carries purpose, time
  range, target, agent id, task, and timeout — future additions
  (caller context, tool-call budgets, tracing ids) can land
  without another signature change. **Breaking** for anyone who
  wrote a custom `SubagentBackend`, but there are no such
  external callers yet so the cost is internal churn only (mock
  subagents + call sites).
- **`SubagentCLIBackendConfig` / `SubagentHTTPBackendConfig`
  grow template fields.** New `retrieve_prompt_template` /
  `write_prompt_template` strings, both optional. Empty means
  "use the built-in default". Same fields added to `AgentSpec`
  as per-agent overrides.
- **Per-parameter tool descriptions rewritten across every
  `pf_*` tool.** Descriptions now include *how to construct* a
  good value with examples — e.g. `pf_scan.query` explains that
  pagefault backends are keyword/substring engines and gives
  2-6-token phrasing guidance; `pf_peek.uri` warns against
  reconstructing URIs instead of copying from a `pf_scan` hit;
  `pf_fault.query` tells the agent it does NOT need to
  rephrase into "search for X" because the server-side prompt
  template already does that. This is the fix for "agent
  doesn't know what to pass" from the deployment review.
- **Agent-selection guidance (from the second trace).**
  `pf_fault.agent` and `pf_poke.agent` descriptions now
  prescribe the `pf_ps` → pick-by-description flow as
  mandatory whenever more than one subagent is configured,
  rather than offering the silent "first configured agent"
  fallback as the default. `pf_ps`'s tool description leads
  with its routing role ("call this **before** `pf_fault` /
  `pf_poke` mode:agent whenever more than one agent is
  configured"). `DefaultInstructions` grows a matching bullet
  in the per-tool guide and a new "Multi-agent routing" note
  that spells out the "check pf_ps, compare the agent
  descriptions against the user's question, pick the best
  match" sequence. The fallback still exists (single-agent
  configs keep working without a `pf_ps` round-trip) but the
  documentation no longer treats it as the default choice.
- **Timeout-floor guidance (from the second trace).**
  `pf_fault.timeout_seconds` and `pf_poke.timeout_seconds`
  descriptions rewritten to establish a 120s floor with
  context on observed deep-retrieval latency (typically
  20-40s just for the subagent to start replying, 60s+ for
  anything that fans out across multiple memory sources).
  The old "lower to 30-60 for simple recalls" guidance
  actively caused truncated runs in the real trace and has
  been removed; the new wording nudges the opposite
  direction (raise to 180-300 for hard lookups, never go
  below 120). The default stays at 120s, matching the
  constant in `deep_retrieve.go`.
- **Proactive discovery: chat-history framing and
  cross-language signal phrases (from the third trace).**
  A third trace caught Claude answering zh-CN queries like
  "我三月在干嘛" and "我最近和你聊了什么餐馆" from its own
  context window and replying "I don't remember" — because
  the 0.6.0 `DefaultInstructions` (a) did not mention that
  pagefault commonly stores past conversations (via
  lossless-lcm and similar), (b) had no rule against false
  "no memory" answers, and (c) only had English signal
  phrases. `internal/tool/instructions.go` was rewritten:
  - Intro now explicitly names the past-conversation
    archive as a first-class citizen of pagefault's store,
    with lossless-lcm / transcripts / embedding indices
    as concrete examples.
  - New **Core rule** section near the top forbids the
    "I don't remember" / "我不记得" / "no record" answers
    without first calling `pf_scan` or `pf_fault`.
  - Signal-phrase list split into **English** and
    **Chinese** subsections covering the same query
    shapes ("我[时间]在干嘛", "我最近和你聊了什么[X]",
    etc.) so zh-CN users' questions pattern-match the
    instructions.
  - New **Temporal references matter** callout: any
    question combining a past-time marker ("last week",
    "三月", "最近", "上周") with a first-person verb
    should route to pagefault by default.
  - The existing multi-agent routing, tool-picking guide,
    and practical guidance sections survive unchanged.
  - `pf_scan`'s entry in the tool-picking guide now
    includes a note that empty results on a
    sentence-shaped query are expected (pf_scan is a
    grep, not semantic search) and the correct response
    is to fall through to pf_fault rather than give up.
  - New `docs/config-doc.md` subsection "Instructions
    override: worked example" gives a concrete YAML
    snippet operators can copy for installation-specific
    framing (naming real backends, routing queries to
    specific agents by name), and explicitly documents
    that the override is a full replace of the default
    rather than a layer.
  Nothing in the above changes the wire shape — it is all
  text in the MCP `initialize` response.
- `handleWriteAgent` dropped `composeAgentWriteTask` — the
  prose-wrapping hack that inlined target + caller label into
  the task string. The write prompt template now carries that
  framing, so the task passed to Spawn is the raw content
  verbatim and the wrapping is applied consistently whether the
  agent was invoked via pf_fault or pf_poke.

### Tests
- `TestServer_SSE_Handshake` — GET /sse returns
  `text/event-stream` and the first event is an `endpoint` event
  with a `sessionId=` query parameter.
- `TestServer_SSE_InitializeRoundtrip` — full three-step flow:
  open SSE, parse endpoint event, POST initialize to the message
  URL, read the response back from the stream. Asserts the
  instructions string flows through to the initialize result so
  both features are tested end-to-end together.
- `TestServer_SSE_DisabledReturns404` — explicit
  `sse_enabled: false` removes the route and streamable-http still
  works (i.e. disabling SSE does not cross-wound `/mcp`).
- `TestServer_SSE_Disabled_RootLandingHidesIt` — the `/` landing
  page only lists `/sse` + `/message` when the transport is live.
- `TestServer_MCP_InstructionsInInitialize` — streamable-http
  `initialize` response contains the distinctive phrase from
  `DefaultInstructions`.
- `TestServer_MCP_InstructionsOverride` — config-supplied
  instructions replace the default (and the default text does not
  leak through).
- `TestDefaultInstructionsNotEmpty` — belt-and-braces guard against
  someone blanking the constant in a refactor. Companions guard
  the review-cycle fixes:
  `TestDefaultInstructions_MultiAgentRouting` asserts the default
  text mentions `pf_ps`, has a "Multi-agent" section, warns against
  the "first configured" fallback, and pins the 120s timeout floor.
  `TestDefaultInstructions_ChatHistoryFraming` asserts the intro
  mentions past conversations AND names at least one concrete
  chat-archive mechanism (lossless-lcm / transcripts / embedding).
  `TestDefaultInstructions_NoFalseNoMemoryClaim` asserts the
  "don't say 'I don't remember' without checking" rule is present
  and lives under a prominent section heading. And
  `TestDefaultInstructions_CrossLanguageSignalPhrases` asserts the
  default contains at least one zh signal phrase, still contains
  at least one English signal phrase, and has a "Temporal"
  references section.
- `internal/backend/prompt_test.go` — nine new tests covering:
  default templates non-empty + distinct + contain the key
  framing phrases, `ResolvePromptTemplate` three-layer
  precedence, `WrapTask` placeholder substitution + empty-
  time-range line collapse + unknown-placeholder passthrough +
  empty-template task echo, `agentPromptOverride` purpose
  routing, and an end-to-end echo test that proves the built-in
  retrieval template reaches the subprocess when no override
  is configured.
- `internal/dispatcher/subagent_test.go` — new
  `TestDispatcher_DeepRetrieve_TimeRangePassthrough`,
  `TestDispatcher_DelegateWrite`,
  `TestDispatcher_DelegateWrite_EmptyContent`. All existing
  `DeepRetrieve` tests updated for the new signature and
  assert on `lastReq.Purpose == SpawnPurposeRetrieve`.
- `internal/tool/deep_retrieve_test.go` — new
  `TestHandleDeepRetrieve_TimeRangePassthrough` table-driven
  test covering all four start/end combinations plus the
  whitespace-only edge case. `stubSubagent` records the full
  `SpawnRequest` so other tests can assert on `lastReq.Purpose`
  as well.
- `internal/tool/write_test.go` — `TestHandleWrite_AgentHappyPath`
  now asserts `lastReq.Task == content` (no prose wrapping),
  `lastReq.Target == "daily"`, and
  `lastReq.Purpose == SpawnPurposeWrite`. Removed the
  `composeAgentWriteTask` tests because that helper no longer
  exists.
- `internal/backend/subagent_{cli,http}_test.go` — every test
  updated for the new `SpawnRequest` signature; narrow plumbing
  tests use a new `passthroughTmpl = "{task}"` constant so they
  assert on bare echoed output rather than the default
  retrieval framing.
- `cmd/pagefault/tools_test.go` — the subagent test config
  fixture now sets `retrieve_prompt_template: "{task}"` and
  `write_prompt_template: "{task}"` so the CLI echo tests still
  assert against the bare task string. Without this they would
  assert against ~20 lines of default template framing, which
  is not the behaviour under test.

### Docs
- `docs/config-doc.md` gains a full `server.mcp` section with a
  client ↔ transport cheat-sheet, and a new subsection on
  subagent backend `retrieve_prompt_template` /
  `write_prompt_template` / per-agent overrides (with the
  placeholder list and precedence diagram).
- `docs/api-doc.md` expands its intro to cover all three
  transports (streamable-http, SSE, REST), adds a "Server-level
  instructions" section explaining what agents see on
  `initialize`, documents the `time_range_start` /
  `time_range_end` fields on `pf_fault`, and carries matching
  "always call pf_ps first in multi-agent setups" and "120s
  minimum timeout" notes on both `pf_fault` and `pf_poke`
  mode:agent.
- `README.md` — Claude Desktop snippet switched from
  streamable-http to SSE with a historical note about the
  pre-0.6.0 supergateway workaround; Recent Changes rewritten
  to cover the unified 0.6.0 release.
- `plan.md` §5.5 `pf_fault` gains the time-range fields; §5.7
  `pf_poke` mode:agent notes the write template. §3 principle 6
  updated to mention the template mechanism.
- `CLAUDE.md` directory tree adds `internal/backend/prompt.go`,
  `prompt_test.go`, and `internal/tool/instructions.go`.

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
