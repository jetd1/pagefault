# Changelog

All notable changes to pagefault are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

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
