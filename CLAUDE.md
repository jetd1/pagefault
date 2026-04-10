# CLAUDE.md — Development Guide for AI Agents

> This file is the **primary navigation aid** for AI agents working on pagefault.
> Read this first. Keep it up to date as files are added, moved, or removed.

## Quick Reference

### Build & Test Commands

```bash
make build          # Build ./bin/pagefault
make test           # Run all tests with race detector
make test-verbose   # Verbose test output
make cover          # Coverage report (coverage.html)
make lint           # go vet + gofmt + staticcheck (if installed)
make fmt            # Format all Go files
make run            # Build and run with configs/minimal.yaml
make clean          # Remove build artifacts

bash scripts/smoke.sh  # End-to-end smoke test (builds, runs, curls every endpoint)
```

### Running Locally

```bash
./bin/pagefault serve --config configs/minimal.yaml
./bin/pagefault token create --label "my-device" --tokens-file /tmp/tokens.jsonl
./bin/pagefault token ls --tokens-file /tmp/tokens.jsonl
./bin/pagefault token revoke <id> --tokens-file /tmp/tokens.jsonl
./bin/pagefault --version
```

## Directory Tree

```
pagefault/
├── CLAUDE.md                             # This file — agent dev guide
├── CHANGELOG.md                          # Version history
├── VERSION                               # Current version (single line: 0.3.0)
├── README.md                             # Quick-start guide
├── Makefile                              # build/test/lint/run/clean/smoke targets
├── plan.md                               # Full spec, source of truth
├── go.mod                                # Go module: github.com/jet/pagefault
├── go.sum                                # Module checksums
├── .gitignore                            # Go-standard ignores
│
├── cmd/
│   └── pagefault/
│       ├── main.go                       # CLI entry, top-level dispatch + --version
│       ├── serve.go                      # `serve` subcommand: config → dispatcher → http.Server (wires every backend type)
│       ├── serve_test.go                 # buildDispatcher tests (minimal, unsupported, full Phase-2 stack)
│       ├── token.go                      # `token create/ls/revoke` subcommands
│       ├── token_test.go                 # Token CLI: lifecycle, slugify, maskToken, list/resolve
│       ├── tools.go                      # `maps`/`load`/`scan`/`peek`/`fault`/`ps` — CLI form of the pf_* tools
│       └── tools_test.go                 # Tool CLI tests: text/JSON/env/cwd fallback/no-filter/audit redirect/fault/ps
│
├── internal/
│   ├── config/
│   │   ├── config.go                     # YAML schema structs, Load/Parse, ${ENV} sub, validator
│   │   └── config_test.go                # Config parse/validate/defaults tests
│   │
│   ├── model/
│   │   └── model.go                      # Shared types (Caller) and sentinel errors
│   │
│   ├── backend/
│   │   ├── backend.go                    # Backend interface + Resource/SearchResult/ResourceInfo
│   │   ├── filesystem.go                 # FilesystemBackend: glob, sandbox, auto-tag, search
│   │   ├── filesystem_test.go            # Filesystem backend tests (21 cases)
│   │   ├── http_helpers.go               # Shared template/JSON-path helpers (renderTemplate/walkPath/…)
│   │   ├── http_helpers_test.go          # Helper unit tests (walkPath edge cases, extractResponse variants)
│   │   ├── subagent.go                   # SubagentBackend interface + AgentInfo
│   │   ├── subagent_cli.go               # CLI-spawned subagent (exec.CommandContext + argv template)
│   │   ├── subagent_cli_test.go          # Tokenizer + spawn/timeout/default-agent tests
│   │   ├── subagent_http.go              # HTTP-spawned subagent (POST + response_path extraction)
│   │   ├── subagent_http_test.go         # httptest-backed tests: auth, body template, timeout
│   │   ├── subprocess.go                 # Generic subprocess search backend (rg/grep/plain parse)
│   │   ├── subprocess_test.go            # Parser + exit-code + command-not-found tests
│   │   ├── http.go                       # Generic HTTP search backend (body template, response_path)
│   │   ├── http_test.go                  # httptest-backed happy-path + error cases
│   │   └── testdata/
│   │       └── sample/
│   │           ├── README.md             # Sample file for include/search tests
│   │           ├── notes/daily.md        # Nested include + auto-tag test
│   │           ├── notes/private.md      # Exclude-pattern test file
│   │           └── skipme.txt            # Non-md file to verify include filter
│   │
│   ├── auth/
│   │   ├── auth.go                       # AuthProvider, Bearer/None/TrustedHeader, middleware
│   │   └── auth_test.go                  # Auth provider + middleware + token gen tests
│   │
│   ├── filter/
│   │   ├── filter.go                     # CompositeFilter, PathFilter, TagFilter
│   │   └── filter_test.go                # Filter allow/deny/composite tests
│   │
│   ├── audit/
│   │   ├── audit.go                      # JSONL/Stdout/Nop loggers, SanitizeArgs, NewEntry
│   │   └── audit_test.go                 # Audit logger tests (incl. concurrent writes)
│   │
│   ├── dispatcher/
│   │   ├── dispatcher.go                 # ToolDispatcher: routes tool calls, filter+audit pipeline
│   │   ├── dispatcher_test.go            # Dispatcher tests with mock backend
│   │   └── subagent_test.go              # ListAgents + DeepRetrieve tests (mockSubagent)
│   │
│   ├── tool/
│   │   ├── tool.go                       # Shared helpers: toolResultJSON, toolResultError
│   │   ├── list_contexts.go              # HandleListContexts pure function (wire: pf_maps)
│   │   ├── get_context.go                # HandleGetContext pure function (wire: pf_load)
│   │   ├── search.go                     # HandleSearch pure function (wire: pf_scan)
│   │   ├── read.go                       # HandleRead pure function (wire: pf_peek)
│   │   ├── deep_retrieve.go              # HandleDeepRetrieve pure function (wire: pf_fault)
│   │   ├── list_agents.go                # HandleListAgents pure function (wire: pf_ps)
│   │   ├── mcp.go                        # RegisterMCP: wires pure handlers to mcp-go
│   │   ├── tool_test.go                  # Pure handler tests
│   │   ├── deep_retrieve_test.go         # pf_fault handler tests (stubSubagent)
│   │   └── mcp_test.go                   # MCP registration + toolResult helper tests
│   │
│   └── server/
│       ├── server.go                     # chi router, MCP mount, REST adapter, /health
│       └── server_test.go                # Integration tests via httptest (incl. MCP smoke)
│
├── configs/
│   ├── minimal.yaml                      # Smallest working config (filesystem only, no auth)
│   └── example.yaml                      # Tour of every backend type with inline docs
│
├── docs/
│   ├── api-doc.md                        # Tool reference (Phase 1–2)
│   ├── config-doc.md                     # Full YAML config reference
│   ├── architecture.md                   # Architecture deep dive
│   └── security.md                       # Threat model, auth, filters, audit
│
├── demo-data/
│   ├── README.md                         # Sample content for minimal.yaml
│   └── notes.md                          # Second sample file
│
└── scripts/
    └── smoke.sh                          # End-to-end smoke test script
```

## Architecture Overview

pagefault is a **config-driven memory server**. The binary is a runtime for a YAML config that defines backends, contexts, tools, filters, and auth. See `docs/architecture.md` for the full diagram and request flow.

**Request flow:**

```
Client → chi router → auth middleware → tool handler → dispatcher
                                                          │
                                              AllowURI ──┼── backend.Read/Search
                                                          │
                                              AllowTags ──┼── FilterContent
                                                          │
                                              audit.Log ──┴── response
```

**Key abstractions:**

- **Backend** — data source plugin interface (`internal/backend/backend.go`). Ships five types: `filesystem` (Phase 1), `subprocess`, `http`, `subagent-cli`, `subagent-http` (Phase 2). `SubagentBackend` extends `Backend` with `Spawn`/`ListAgents` for `pf_fault` and `pf_ps`.
- **Context** — named, pre-composed bundle of backend resources (YAML-defined).
- **Filter** — optional path/tag/redaction filter. Can be fully disabled.
- **Auth** — bearer token, trusted header, or none.
- **Dispatcher** — central tool router. Holds backends + contexts + filters + audit logger.
- **Tools** — pure `HandleX` functions (`internal/tool/*.go`); the server package wraps them for REST and `tool.RegisterMCP` wraps them for mcp-go.

## Tool Naming

**Wire names are `pf_*`; internal Go names are generic; CLI names drop
the `pf_` prefix.** The wire surface (MCP/REST/config) uses memorable
page-fault-themed names that namespace cleanly against other MCP
servers; the Go code keeps descriptive `HandleListContexts`/etc. names
for developer clarity; the CLI uses bare verbs because the outer
`pagefault` binary already provides the namespace. The full mapping:

| Wire (MCP/REST/config) | CLI (`pagefault …`) | Go handler / type prefix | Go file (`internal/tool/`) | Phase |
|------------------------|---------------------|--------------------------|----------------------------|-------|
| `pf_maps`              | `maps`              | `HandleListContexts`     | `list_contexts.go`         | 1     |
| `pf_load`              | `load`              | `HandleGetContext`       | `get_context.go`           | 1     |
| `pf_scan`              | `scan`              | `HandleSearch`           | `search.go`                | 1     |
| `pf_peek`              | `peek`              | `HandleRead`             | `read.go`                  | 1     |
| `pf_fault`             | `fault`             | `HandleDeepRetrieve`     | `deep_retrieve.go`         | 2     |
| `pf_ps`                | `ps`                | `HandleListAgents`       | `list_agents.go`           | 2     |
| `pf_poke`              | `poke` *            | `HandleWrite` *          | `write.go` *               | 4     |

(*) Planned — not implemented yet.

The wire name is authoritative for: MCP tool registration, REST routes
(`/api/{wire_name}`), the `tools:` section of the YAML config, and the
`tool` field of audit log entries. The CLI subcommand dispatches through
the same `Handle*` function as REST/MCP, so all three transports share
filter, audit, and error semantics. Never hand-roll a new tool without
updating `internal/config/config.go`'s `ToolsConfig` struct, because that
is how `d.ToolEnabled(name)` finds the toggle.

**CLI semantics:**

- Config lookup: `--config <path>` → `$PAGEFAULT_CONFIG` → `./pagefault.yaml`.
- Filters apply by default (CLI sees what an MCP client would see).
  `--no-filter` is the operator escape hatch.
- Default output is human-readable (tabwriter tables for `maps`/`scan`,
  raw content for `load`/`peek`); `--json` emits machine-readable JSON.
- `audit.mode: stdout` is rewritten to `stderr` in CLI context so the
  data stream stays pipe-clean (`pagefault load demo --json | jq .`).
- Positional args can appear anywhere on the command line — a local
  `parseInterspersed` helper hoists flags past positionals before
  delegating to stdlib `flag`.

## Common Development Tasks

### Adding a new backend type

1. Create `internal/backend/<type>.go` implementing the `Backend` interface from `internal/backend/backend.go`.
2. Add a type-specific config struct to `internal/config/config.go` (e.g., `SubprocessBackendConfig`) plus a `Decode<Type>Backend` helper.
3. Register the type in `cmd/pagefault/serve.go` → `buildDispatcher`.
4. Add tests in `internal/backend/<type>_test.go`.
5. Update `docs/config-doc.md` with the new type's YAML fields.
6. Update `CLAUDE.md` directory tree.
7. Bump version (minor) and add CHANGELOG entry.

### Adding a new tool

Pick a wire name first (`pf_<something>`) — it's what shows up on MCP,
REST, the config yaml, and the audit log. The CLI form drops the `pf_`
prefix (`pagefault <something>`). Keep the Go handler name
generic/descriptive (`HandleX`) — it's internal and doesn't need to carry
the `pf_` prefix.

1. Create `internal/tool/<descriptive>.go` with a `HandleX` pure function (transport-agnostic).
2. Add a dispatcher method if the routing logic doesn't already exist (and remember to pass the wire name as the `tool` field to `audit.NewEntry`).
3. Register the tool with the MCP server in `internal/tool/mcp.go` using `mcppkg.NewTool("pf_<name>", ...)`.
4. Add a REST route `/api/pf_<name>` in `internal/server/server.go` using `restHandler(d, tool.HandleX)`.
5. Add a `Pf<Name> *bool` field to `ToolsConfig` in `internal/config/config.go` with `yaml:"pf_<name>,omitempty"` and a case in the `Enabled` switch.
6. Add a `run<Name>` function and text/JSON formatter in `cmd/pagefault/tools.go`, and a dispatch case in `cmd/pagefault/main.go` + the `usage()` text.
7. Add the wire ↔ CLI ↔ code row to the "Tool Naming" table in this file.
8. Add tests: `internal/tool/<descriptive>_test.go` (pure handler), `internal/tool/mcp_test.go` (MCP registration), `cmd/pagefault/tools_test.go` (CLI subcommand).
9. Update `docs/api-doc.md` (both the HTTP section and the CLI section).
10. Update `CLAUDE.md` directory tree.
11. Bump version (minor) and add CHANGELOG entry.

### Adding a new filter

1. Create the filter type in `internal/filter/filter.go` implementing the `Filter` interface.
2. Add the config struct to `internal/config/config.go`.
3. Wire it into `filter.NewFromConfig`.
4. Add tests in `internal/filter/filter_test.go`.
5. Update `docs/config-doc.md`.
6. Bump version.

## Conventions

- **Go style:** `gofmt`, `go vet`, `staticcheck` must pass. Run `make lint`.
- **Interfaces:** accept interfaces, return concrete structs.
- **Context:** `context.Context` as first param in all I/O methods.
- **Errors:** use `fmt.Errorf("...: %w", err)` for wrapping. Sentinel errors in `internal/model/model.go`. Check with `errors.Is` / `errors.As`.
- **Logging:** `log/slog` only. No `fmt.Println` in library code (CLI output is fine in `cmd/`).
- **Naming:** Go conventions — `NewFilesystemBackend`, not `CreateFilesystemBackend`. Acronyms all-caps (`URI`, `HTTP`).
- **Comments:** Godoc on all exported types and functions. Package-level doc comment in every package.
- **Tests:** live alongside source files. Table-driven preferred. Test data in `testdata/` directories.
- **Commits:** conventional commits (`feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`).

## Rules (What NOT to Do)

- Do NOT import from OpenClaw, Hermes, or any deployment-specific package
- Do NOT hardcode paths, URLs, IPs, or user identifiers in code — use config
- Do NOT assume a specific OS, shell, or filesystem layout
- Do NOT add caching in Phase 1 (YAGNI)
- Do NOT add streaming responses in Phase 1
- Do NOT build Docker/systemd/Caddy configs — post-deploy infra, not part of the binary
- Do NOT skip writing tests
- Do NOT change config schema without updating `docs/config-doc.md`
- Do NOT add a tool without updating `docs/api-doc.md`

## Versioning

- Single-line `VERSION` file at repo root. Binary echoes it via `pagefault --version`.
- **Bump before every behavioral commit:** patch for fixes/tweaks, minor for new features/backends/tools, never major without explicit ask.
- Update `CHANGELOG.md` whenever version changes.
- Keep the 3 most recent changelog entries in `README.md` under "Recent Changes".
- Before bumping: `make test` passes, `make lint` passes, all docs updated, directory tree in this file matches reality.

## See Also

- `plan.md` — full spec, source of truth for Phase 1–5
- `docs/architecture.md` — architecture deep dive
- `docs/api-doc.md` — tool reference
- `docs/config-doc.md` — config reference
- `docs/security.md` — threat model, auth, filters, audit
- `README.md` — user-facing quick start
