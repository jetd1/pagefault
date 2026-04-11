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
# OAuth2 client_credentials (auth.mode: oauth2) — 0.7.0+
./bin/pagefault oauth-client create --label "Claude Desktop" --clients-file /tmp/oauth-clients.jsonl
./bin/pagefault oauth-client ls --clients-file /tmp/oauth-clients.jsonl
./bin/pagefault oauth-client revoke <id> --clients-file /tmp/oauth-clients.jsonl
./bin/pagefault --version
```

## Directory Tree

```
pagefault/
├── CLAUDE.md                             # This file — agent dev guide
├── CHANGELOG.md                          # Version history
├── VERSION                               # Current version (single line, read by the Makefile and -ldflags)
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
│       ├── oauth_client.go               # `oauth-client create/ls/revoke` subcommands (0.7.0): manage OAuth2 client_credentials registry, bcrypt secret hashing, prints secret once
│       ├── oauth_client_test.go          # OAuth-client CLI tests: full lifecycle, duplicate-id, empty list, stored-hash-verifies-printed-secret, resolveClientsFile precedence
│       ├── tools.go                      # `maps`/`load`/`scan`/`peek`/`fault`/`ps`/`poke` — CLI form of the pf_* tools
│       └── tools_test.go                 # Tool CLI tests: text/JSON/env/cwd fallback/no-filter/audit redirect/fault/ps/poke
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
│   │   ├── backend.go                    # Backend / HealthChecker / WritableBackend interfaces + Resource/SearchResult/ResourceInfo
│   │   ├── filesystem.go                 # FilesystemBackend: glob, sandbox, auto-tag, search, Phase-4 write path
│   │   ├── filesystem_test.go            # Filesystem backend read-path tests
│   │   ├── filesystem_write_test.go      # Filesystem backend write-path tests (Phase 4)
│   │   ├── http_helpers.go               # Shared template/JSON-path helpers (renderTemplate/walkPath/…)
│   │   ├── http_helpers_test.go          # Helper unit tests (walkPath edge cases, extractResponse variants)
│   │   ├── subagent.go                   # SubagentBackend interface + AgentInfo + SpawnRequest
│   │   ├── prompt.go                     # SpawnRequest / SpawnPurpose, default retrieve+write prompt templates, ResolvePromptTemplate + WrapTask
│   │   ├── prompt_test.go                # Template precedence, placeholder substitution, end-to-end default-template echo
│   │   ├── subagent_cli.go               # CLI-spawned subagent (exec.CommandContext + argv template)
│   │   ├── subagent_cli_test.go          # Tokenizer + spawn/timeout/default-agent tests (passthroughTmpl-based plumbing tests)
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
│   │   ├── auth_test.go                  # Auth provider + middleware + token gen tests
│   │   ├── oauth2.go                     # OAuth2 client_credentials provider (0.7.0): ClientRecord, IssuedToken store, IssueToken/Authenticate, compound-mode fallback
│   │   └── oauth2_test.go                # OAuth2 provider unit tests (happy/invalid/expired/compound/scope intersection/reload)
│   │
│   ├── filter/
│   │   ├── filter.go                     # CompositeFilter, PathFilter (read + Phase-4 write globs), TagFilter, RedactionFilter
│   │   └── filter_test.go                # Filter allow/deny/composite/AllowWriteURI tests
│   │
│   ├── write/
│   │   ├── writer.go                     # Writer interface + FilesystemWriter (flock, atomic append) — Phase 4
│   │   ├── writer_test.go                # FilesystemWriter happy-path + concurrency + cancel tests
│   │   ├── format.go                     # FormatEntry (entry / raw templating) — Phase 4
│   │   └── format_test.go                # FormatEntry tests (fixed clock, templating edge cases)
│   │
│   ├── audit/
│   │   ├── audit.go                      # JSONL/Stdout/Nop loggers, SanitizeArgs, NewEntry
│   │   └── audit_test.go                 # Audit logger tests (incl. concurrent writes)
│   │
│   ├── dispatcher/
│   │   ├── dispatcher.go                 # ToolDispatcher: routes tool calls (read + write), filter+audit pipeline
│   │   ├── dispatcher_test.go            # Dispatcher tests with mock backend (incl. writable mock + Write)
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
│   │   ├── write.go                      # HandleWrite pure function (wire: pf_poke) — Phase 4
│   │   ├── mcp.go                        # RegisterMCP: wires pure handlers to mcp-go
│   │   ├── instructions.go               # DefaultInstructions — server-level MCP initialize text
│   │   ├── tool_test.go                  # Pure handler tests
│   │   ├── deep_retrieve_test.go         # pf_fault handler tests (stubSubagent)
│   │   ├── write_test.go                 # pf_poke direct/agent handler tests — Phase 4
│   │   └── mcp_test.go                   # MCP registration + toolResult helper tests (incl. pf_poke)
│   │
│   └── server/
│       ├── server.go                     # chi router, MCP streamable-http + SSE mounts, REST adapter, /health, structured error envelope, oauth2 route mounts when enabled
│       ├── server_test.go                # Integration tests via httptest (incl. MCP smoke, SSE handshake + roundtrip, instructions, OpenAPI, health probe)
│       ├── oauth2.go                     # OAuth2 discovery + token endpoints (0.7.0): /.well-known/oauth-protected-resource (RFC 9728), /.well-known/oauth-authorization-server (RFC 8414), POST /oauth/token (client_credentials, Basic + form body)
│       ├── oauth2_test.go                # OAuth2 HTTP integration: discovery shape, Basic/form/invalid/unsupported/missing-creds paths, end-to-end token → /api/pf_maps, compound mode, unmounted-when-disabled, URL-encoded Basic credential decoding
│       ├── openapi.go                    # /api/openapi.json spec builder (OpenAPI 3.1.0, live from dispatcher + config)
│       ├── cors.go                       # CORS middleware (opt-in, per server.cors config)
│       ├── cors_test.go                  # Preflight + allowed/denied origin tests
│       ├── ratelimit.go                  # Per-caller token bucket middleware (golang.org/x/time/rate)
│       └── ratelimit_test.go             # 429 envelope, Retry-After, separate-caller buckets
│
├── configs/
│   ├── minimal.yaml                      # Smallest working config (filesystem only, no auth)
│   └── example.yaml                      # Tour of every backend type with inline docs
│
├── docs/
│   ├── api-doc.md                        # Tool reference (Phase 1–4)
│   ├── config-doc.md                     # Full YAML config reference (incl. Phase-4 write fields)
│   ├── architecture.md                   # Architecture deep dive
│   └── security.md                       # Threat model, auth, filters, audit, rate limit, CORS, write safety
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

- **Backend** — data source plugin interface (`internal/backend/backend.go`). Ships five types: `filesystem` (Phase 1, Phase-4 write support), `subprocess`, `http`, `subagent-cli`, `subagent-http` (Phase 2). `SubagentBackend` extends `Backend` with `Spawn(ctx, SpawnRequest)` / `ListAgents` for `pf_fault` and `pf_ps`. `WritableBackend` is an optional Phase-4 extension implemented by `FilesystemBackend` when `writable: true`.
- **SpawnRequest / prompt templates** — `internal/backend/prompt.go` defines the `SpawnRequest` struct (agent id, task, purpose, time range, target, timeout), the `SpawnPurpose` enum (`retrieve` / `write`), the two default templates, and `ResolvePromptTemplate` + `WrapTask`. Every subagent backend wraps the raw caller content with a resolved template **before** substituting into its command / body template, so fresh subagents are framed as memory retrievers/placers rather than generic Q&A. Three-layer override precedence: per-agent → per-backend → built-in default.
- **Context** — named, pre-composed bundle of backend resources (YAML-defined).
- **Filter** — optional path/tag/redaction filter. Can be fully disabled. Phase-4 added `AllowWriteURI` for the write path.
- **Auth** — bearer token, trusted header, OAuth2 client_credentials (0.7.0+, `internal/auth/oauth2.go`), or none. OAuth2 mode mounts three public endpoints (`/.well-known/oauth-protected-resource`, `/.well-known/oauth-authorization-server`, `POST /oauth/token`) and runs as a compound provider: it validates issued access tokens first, then falls back to `BearerTokenAuth` when `bearer.tokens_file` is also configured. Clients are registered via `pagefault oauth-client create` and stored with bcrypt secret hashes.
- **Dispatcher** — central tool router. Holds backends + contexts + filters + audit logger. Exposes `ListContexts`, `GetContext`, `Search`, `Read`, `DeepRetrieve`, `ListAgents`, `Write`, and `DelegateWrite` (Phase-4 write-side twin of `DeepRetrieve` that spawns a subagent with `Purpose=write` so the write-framed prompt template is picked).
- **Writer** — `internal/write.FilesystemWriter` is the flock-serialised atomic-append primitive behind `pf_poke` mode:direct.
- **Tools** — pure `HandleX` functions (`internal/tool/*.go`); the server package wraps them for REST and `tool.RegisterMCP` wraps them for mcp-go.
- **Instructions** — `internal/tool/instructions.go` holds `DefaultInstructions`, the server-level text advertised in the MCP `initialize` response (via `mcpserver.WithInstructions`). Operators override via `server.mcp.instructions` in YAML. This is the highest-leverage lever for teaching cold agents when to reach for `pf_*` tools vs their built-ins; it covers chat-history framing, a core "don't claim no memory" rule, cross-language signal phrases, multi-agent routing, and a 120s timeout floor. Edit with care — the server-wide test suite pins several phrases in place.

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
| `pf_poke`              | `poke`              | `HandleWrite`            | `write.go`                 | 4     |

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
