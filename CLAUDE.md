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
├── VERSION                               # Current version (single line: 0.1.0)
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
│       ├── serve.go                      # `serve` subcommand: config → dispatcher → http.Server
│       ├── token.go                      # `token create/ls/revoke` subcommands
│       └── token_test.go                 # Token CLI and slugify/maskToken tests
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
│   │   └── dispatcher_test.go            # Dispatcher tests with mock backend
│   │
│   ├── tool/
│   │   ├── tool.go                       # Shared helpers: toolResultJSON, toolResultError
│   │   ├── list_contexts.go              # HandleListContexts pure function
│   │   ├── get_context.go                # HandleGetContext pure function
│   │   ├── search.go                     # HandleSearch pure function
│   │   ├── read.go                       # HandleRead pure function
│   │   ├── mcp.go                        # RegisterMCP: wires pure handlers to mcp-go
│   │   └── tool_test.go                  # Tool handler tests
│   │
│   └── server/
│       ├── server.go                     # chi router, MCP mount, REST adapter, /health
│       └── server_test.go                # Integration tests via httptest (incl. MCP smoke)
│
├── configs/
│   └── minimal.yaml                      # Smallest working config (no auth, demo data)
│
├── docs/
│   ├── api-doc.md                        # Phase-1 tool reference
│   ├── config-doc.md                     # Full YAML config reference
│   └── architecture.md                   # Architecture deep dive
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

- **Backend** — data source plugin interface (`internal/backend/backend.go`). Phase 1 ships the filesystem backend. Phases 2+ add subprocess, HTTP, and subagent backends.
- **Context** — named, pre-composed bundle of backend resources (YAML-defined).
- **Filter** — optional path/tag/redaction filter. Can be fully disabled.
- **Auth** — bearer token, trusted header, or none.
- **Dispatcher** — central tool router. Holds backends + contexts + filters + audit logger.
- **Tools** — pure `HandleX` functions (`internal/tool/*.go`); the server package wraps them for REST and `tool.RegisterMCP` wraps them for mcp-go.

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

1. Create `internal/tool/<tool>.go` with a `HandleX` pure function (transport-agnostic).
2. Add a dispatcher method if the routing logic doesn't already exist.
3. Register the tool with the MCP server in `internal/tool/mcp.go`.
4. Add a REST route in `internal/server/server.go` using `restHandler(d, tool.HandleX)`.
5. Add the tool name to `ToolsConfig` in `internal/config/config.go` and `Enabled` switch.
6. Add tests in `internal/tool/<tool>_test.go`.
7. Update `docs/api-doc.md`.
8. Update `CLAUDE.md` directory tree.
9. Bump version (minor) and add CHANGELOG entry.

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
- `README.md` — user-facing quick start
