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
# OAuth2 (auth.mode: oauth2) — client_credentials 0.7.0+, authorization_code + PKCE 0.8.0+
./bin/pagefault oauth-client create --label "CI" --clients-file /tmp/oauth-clients.jsonl
./bin/pagefault oauth-client create --label "Claude Desktop" --public \
    --redirect-uris "http://localhost:3000/callback" --clients-file /tmp/oauth-clients.jsonl
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
├── go.mod                                # Go module: github.com/jet/pagefault
├── go.sum                                # Module checksums
├── .gitignore                            # Go-standard ignores
│
├── cmd/
│   └── pagefault/
│       ├── main.go                       # CLI entry, top-level dispatch + --version
│       ├── serve.go                      # `serve` subcommand: config → task.Manager → dispatcher → http.Server (wires every backend type; 0.10.0 constructs the async task manager from server.tasks config and routes shutdown through dispatcher.Close)
│       ├── serve_test.go                 # buildDispatcher tests (minimal, unsupported, full Phase-2 stack)
│       ├── token.go                      # `token create/ls/revoke` subcommands
│       ├── token_test.go                 # Token CLI: lifecycle, slugify, maskToken, list/resolve
│       ├── oauth_client.go               # `oauth-client create/ls/revoke` subcommands (0.7.0+, 0.8.0 added --public + --redirect-uris, 0.9.0 added SOURCE column): manage OAuth2 client registry (confidential + public clients for client_credentials and authorization_code grants), bcrypt secret hashing, prints secret once
│       ├── oauth_client_test.go          # OAuth-client CLI tests: full lifecycle, duplicate-id, empty list, stored-hash-verifies-printed-secret, resolveClientsFile precedence, public client + redirect_uris path, TYPE/REDIRECT_URIS/SOURCE columns
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
│   │   ├── prompt.go                     # SpawnRequest / SpawnPurpose (0.10.0 added SpawnID field), default retrieve+write prompt templates, ResolvePromptTemplate + WrapTask
│   │   ├── prompt_test.go                # Template precedence, placeholder substitution, end-to-end default-template echo
│   │   ├── subagent_cli.go               # CLI-spawned subagent (exec.CommandContext + argv template, 0.10.0 added {spawn_id} substitution)
│   │   ├── subagent_cli_test.go          # Tokenizer + spawn/timeout/default-agent tests (passthroughTmpl-based plumbing tests, 0.10.0 added spawn_id passthrough + unused-when-absent)
│   │   ├── subagent_http.go              # HTTP-spawned subagent (POST + response_path extraction, 0.10.0 added {spawn_id} substitution in URL path + body template)
│   │   ├── subagent_http_test.go         # httptest-backed tests: auth, body template, timeout, 0.10.0 spawn_id round-trip
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
│   │   ├── oauth2.go                     # OAuth2 provider (0.7.0+, 0.9.0 added DCR): ClientRecord, IssuedToken + AuthorizationCode stores, IssueToken / IssueAuthorizationCode / ExchangeAuthorizationCode / Authenticate / RegisterClient, S256 PKCE verification (crypto/subtle), compound-mode fallback to BearerTokenAuth, file-based reload + RevokeClient sweep, RFC 7591 Dynamic Client Registration (public-only, pf_dcr_ prefix, JSONL persistence)
│   │   └── oauth2_test.go                # OAuth2 provider unit tests: client_credentials (happy/invalid/expired/compound/scope intersection/reload), authorization_code + PKCE (happy/unknown-client/unregistered-URI/expired/consumed/wrong-redirect/wrong-client/RFC 7636 Appendix B test vector), public-client lifecycle, DCR (happy/validation/persistence/concurrent/bearer-token-gate)
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
│   ├── task/
│   │   ├── task.go                       # 0.10.0 in-memory async task manager: Task/Status/Config, Manager with Submit/Get/Wait/Close/sweep, TimeoutError sentinel, GenerateSpawnID (pf_sp_* token). Runs every pf_fault / pf_poke mode:agent spawn on a background goroutine detached from the caller's HTTP request; max_concurrent backpressure → ErrBackpressure; TTL sweep on Submit/Get/Wait.
│   │   └── task_test.go                  # Happy/failure/timeout/detached-context/max-concurrent/unknown/close-cancels-in-flight/sweep-expired/sweep-keeps-running/concurrent-stress/GenerateSpawnID/defaults tests (all under -race)
│   │
│   ├── dispatcher/
│   │   ├── dispatcher.go                 # ToolDispatcher: routes tool calls (read + write), filter+audit pipeline, 0.10.0 async DeepRetrieve/DelegateWrite via task.Manager with Wait flag + GetTask poll entry point; dispatcher.Close now shuts the task manager first
│   │   ├── dispatcher_test.go            # Dispatcher tests with mock backend (incl. writable mock + Write)
│   │   └── subagent_test.go              # ListAgents + DeepRetrieve tests (mockSubagent with mutex-guarded lastReq); 0.10.0 Async + GetTask tests
│   │
│   ├── tool/
│   │   ├── tool.go                       # Shared helpers: toolResultJSON, toolResultError
│   │   ├── list_contexts.go              # HandleListContexts pure function (wire: pf_maps)
│   │   ├── get_context.go                # HandleGetContext pure function (wire: pf_load)
│   │   ├── search.go                     # HandleSearch pure function (wire: pf_scan)
│   │   ├── read.go                       # HandleRead pure function (wire: pf_peek)
│   │   ├── deep_retrieve.go              # HandleDeepRetrieve pure function (wire: pf_fault; 0.10.0 reshape: async-by-default with Wait:true sync compat flag, output carries task_id/status/spawn_id)
│   │   ├── list_agents.go                # HandleListAgents + HandleTaskStatus pure functions (wire: pf_ps; 0.10.0 extended with optional TaskID — empty = list agents, set = poll task snapshot)
│   │   ├── write.go                      # HandleWrite pure function (wire: pf_poke) — Phase 4; pf_poke mode:agent passes Wait:true so writes return synchronously
│   │   ├── mcp.go                        # RegisterMCP: wires pure handlers to mcp-go. 0.10.0 added wait flag to pf_fault + task_id to pf_ps + routing between HandleListAgents/HandleTaskStatus, plus asBool coercion helper
│   │   ├── instructions.go               # DefaultInstructions — server-level MCP initialize text
│   │   ├── tool_test.go                  # Pure handler tests
│   │   ├── deep_retrieve_test.go         # pf_fault handler tests (stubSubagent with mutex-guarded snapshot); 0.10.0 added async-default, task-status-unknown, sync-wait variants
│   │   ├── write_test.go                 # pf_poke direct/agent handler tests — Phase 4
│   │   └── mcp_test.go                   # MCP registration + toolResult helper tests (incl. pf_poke)
│   │
│   └── server/
│       ├── server.go                     # chi router, MCP streamable-http + SSE mounts, REST adapter, /health, structured error envelope, oauth2 route mounts when enabled
│       ├── server_test.go                # Integration tests via httptest (incl. MCP smoke, SSE handshake + roundtrip, instructions, OpenAPI, health probe)
│       ├── oauth2.go                     # OAuth2 HTTP surface (0.7.0+, 0.8.0 added authorize + auth code flow, 0.8.1 hardened validation order + consent, 0.9.0 added DCR): /.well-known/oauth-protected-resource (RFC 9728), /.well-known/oauth-authorization-server (RFC 8414, includes registration_endpoint when DCR enabled), POST /oauth/token (client_credentials + authorization_code, Basic + form body), GET+POST /oauth/authorize (consent page + auto-approve + PKCE), POST /register (RFC 7591 DCR, public-only, opt-in), consentParams whitelist, errors.Is classification
│       ├── oauth2_test.go                # OAuth2 HTTP integration: discovery shape, Basic/form/invalid/unsupported/missing-creds paths, end-to-end token → /api/pf_maps, compound mode, unmounted-when-disabled, URL-encoded Basic credential decoding, authorize happy paths (auto-approve + public/confidential auth code flow), DCR (happy/validation/disabled/bearer-gate/discovery/end-to-end), negative regressions (open redirect on bad response_type or missing client_id, consent-form action-injection bypass, consent default-deny, frame-ancestors CSP)
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
│   ├── api-doc.md                        # Tool reference (HTTP + CLI) for all seven `pf_*` tools
│   ├── config-doc.md                     # Full YAML config reference (incl. filesystem write fields + OAuth2)
│   ├── architecture.md                   # Architecture deep dive (request flow, backend model, prompt templates, transports, OAuth2 wiring)
│   ├── security.md                       # Threat model, auth (bearer / trusted_header / oauth2 / none), filters, audit, rate limit, CORS, write safety
│   └── design.md                         # Design system — concept, voice, color, type, icons, spacing, motion, a11y (governs `web/` + any future user-facing surface)
│
├── demo-data/
│   ├── README.md                         # Sample content for minimal.yaml
│   └── notes.md                          # Second sample file
│
├── web/                                  # Static landing site (governed by docs/design.md) — embedded into the binary via //go:embed and served at / by internal/server
│   ├── embed.go                          # `package web` — `//go:embed` directive exporting `Files embed.FS` consumed by internal/server
│   ├── index.html                        # Landing page — hero + concept + tools table (inline glyphs) + quickstart + transports + architecture + outro
│   ├── styles.css                        # Full stylesheet — design tokens, components, sections, reduced-motion, print
│   ├── script.js                         # Hero terminal animation (cycles pf_fault → fault → handler → resolved; IntersectionObserver pause, prefers-reduced-motion honored)
│   ├── favicon.svg                       # Logomark — rounded page w/ diagonal fault slice + inward load chevron; ships `#mark` + `#mark-16` symbols
│   └── icons.svg                         # Tool-glyph sprite — seven `<symbol>`s (maps/load/scan/peek/fault/ps/poke) referenced via `<use href="./icons.svg#X">`
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
- **SpawnRequest / prompt templates** — `internal/backend/prompt.go` defines the `SpawnRequest` struct (agent id, task, purpose, time range, target, spawn id, timeout), the `SpawnPurpose` enum (`retrieve` / `write`), the two default templates, and `ResolvePromptTemplate` + `WrapTask`. Every subagent backend wraps the raw caller content with a resolved template **before** substituting into its command / body template, so fresh subagents are framed as memory retrievers/placers rather than generic Q&A. Three-layer override precedence: per-agent → per-backend → built-in default. 0.10.0 added the `{spawn_id}` placeholder (a fresh `pf_sp_*` random token per call) that `subagent-cli` substitutes into argv and `subagent-http` substitutes into URL path + body template, so operators can wire it into their agent runner's session flag (e.g. `openclaw agent run --session-id {spawn_id}`) to force session isolation per call.
- **Task manager (0.10.0+)** — `internal/task.Manager` runs every `pf_fault` / `pf_poke mode:agent` subagent spawn on a goroutine detached from the caller's HTTP request context. `dispatcher.DeepRetrieve` submits to the manager by default and returns `{task_id, status: "running"}` immediately; callers poll via `pf_ps(task_id=...)` → `dispatcher.GetTask`. The dispatcher's `DeepRetrieveOptions.Wait: true` flag (set by CLI and `pf_poke mode:agent`) routes through `Manager.Wait` instead so the call blocks on terminal status. Config lives under `server.tasks.{ttl_seconds, max_concurrent}`; defaults are 600s TTL and 16 concurrent. Manager is in-memory only — restart loses in-flight tasks.
- **Context** — named, pre-composed bundle of backend resources (YAML-defined).
- **Filter** — optional path/tag/redaction filter. Can be fully disabled. Phase-4 added `AllowWriteURI` for the write path.
- **Auth** — bearer token, trusted header, OAuth2 (0.7.0+, `internal/auth/oauth2.go`), or none. OAuth2 supports two grants: **client_credentials** (0.7.0) for programmatic clients with a bcrypt-hashed secret, and **authorization_code + PKCE (S256-only)** (0.8.0) for browser-based clients like Claude Desktop. OAuth2 mode mounts four public endpoints (`/.well-known/oauth-protected-resource`, `/.well-known/oauth-authorization-server`, `POST /oauth/token`, `GET+POST /oauth/authorize`) plus an optional fifth — `POST /register` (RFC 7591 Dynamic Client Registration, 0.9.0, opt-in via `dcr_enabled`) — and runs as a compound provider: it validates issued access tokens first, then falls back to `BearerTokenAuth` when `bearer.tokens_file` is also configured. Clients are registered via `pagefault oauth-client create` (confidential by default, `--public --redirect-uris` for PKCE-only public clients) or dynamically via DCR (public-only, `pf_dcr_` prefix).
- **Dispatcher** — central tool router. Holds backends + contexts + filters + audit logger + **task manager** (0.10.0+). Exposes `ListContexts`, `GetContext`, `Search`, `Read`, `DeepRetrieve` (async-by-default with `Wait:true` compat), `ListAgents`, `GetTask` (0.10.0+, returns the snapshot behind `pf_ps(task_id=...)`), `Write`, and `DelegateWrite` (Phase-4 write-side twin of `DeepRetrieve` that spawns a subagent with `Purpose=write` so the write-framed prompt template is picked; `pf_poke` mode:agent sets `Wait:true` to preserve synchronous return semantics).
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

- `docs/architecture.md` — architecture deep dive (request flow, backend + subagent model, prompt templates, transports)
- `docs/api-doc.md` — tool reference (HTTP + CLI for all seven `pf_*` tools)
- `docs/config-doc.md` — YAML config reference
- `docs/security.md` — threat model, auth, filters, audit, write safety
- `CHANGELOG.md` — authoritative version history
- `README.md` — user-facing quick start
