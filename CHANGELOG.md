# Changelog

All notable changes to pagefault are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

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
