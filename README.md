# pagefault

> When your agent hits a context miss, pagefault loads the right page back in.

**pagefault** is a config-driven memory server that exposes personal
knowledge (files, search indices, agent sessions) to external AI clients via
**MCP** and **REST**. The metaphor: in an OS a page fault occurs when a
process accesses memory that isn't resident — the handler fetches it from
backing store and resumes execution. pagefault does the same for AI agents:
when they need context they don't have, they fault to this server, which
loads the right information from configured backends.

Phase 1 is a minimal, working slice: a Go binary that serves markdown files
from a directory, with four tools (`pf_maps`, `pf_load`, `pf_scan`,
`pf_peek`), bearer-token auth, path/tag filters, and JSONL audit logging.
Tool names follow a `pf_*` scheme borrowed from Unix memory management and
kernel debugging — see `docs/api-doc.md` for the mapping.

## Quick start

```bash
# Build
make build

# Drive the tools directly from the CLI — no server needed
./bin/pagefault maps --config configs/minimal.yaml
./bin/pagefault scan pagefault --config configs/minimal.yaml
./bin/pagefault peek memory://README.md --config configs/minimal.yaml
./bin/pagefault load demo --config configs/minimal.yaml

# Or run the server and hit it over HTTP
./bin/pagefault serve --config configs/minimal.yaml
# (in another terminal)
curl -s http://127.0.0.1:8444/health | jq
curl -s -X POST http://127.0.0.1:8444/api/pf_maps | jq
curl -s -X POST http://127.0.0.1:8444/api/pf_scan \
  -H 'Content-Type: application/json' \
  -d '{"query":"pagefault"}' | jq
```

The MCP endpoint is available at `POST /mcp` (streamable-http transport)
when running `serve`. The CLI form (`pagefault peek`, `pagefault scan`, …)
is the same vocabulary minus the `pf_` prefix — see `docs/api-doc.md` for
the full reference.

## Creating a production config

```bash
# 1. Start from a template:
#    - configs/minimal.yaml  — the smallest runnable config (filesystem only)
#    - configs/example.yaml  — a tour of every backend type, with inline docs
cp configs/example.yaml pagefault.yaml

# 2. Enable bearer auth and point it at a tokens file
# (see docs/config-doc.md for every field)

# 3. Create a token for your first client device
./bin/pagefault token create --label "Claude Code" --tokens-file ./tokens.jsonl

# 4. Run
./bin/pagefault serve --config ./pagefault.yaml
```

## Tests and linting

```bash
make test         # full test suite with race detector
make lint         # go vet + gofmt check
make cover        # coverage report (coverage.html)
bash scripts/smoke.sh   # end-to-end smoke test
```

## Documentation

- **`plan.md`** — full product spec and roadmap (source of truth)
- **`docs/api-doc.md`** — tool reference (Phase 1)
- **`docs/config-doc.md`** — full YAML configuration reference
- **`docs/architecture.md`** — architecture deep dive
- **`CLAUDE.md`** — developer guide for AI agents working on this repo

## Recent Changes

### 0.3.1 — 2026-04-10

- **`configs/example.yaml`** added — a documented tour of every backend
  type (filesystem, subprocess, http, subagent-cli, subagent-http) with
  three of them commented out so new users can uncomment what they need.
- **Config test coverage rebound.** `internal/config` went from 54.6%
  → 87.6% after direct unit tests were added for every
  `Decode*Backend` helper (happy path, wrong type, missing required
  fields).
- **Shared HTTP helpers moved to `internal/backend/http_helpers.go`.**
  `renderTemplate`, `jsonEscape`, `walkPath`, `extractResponse` are
  now owned by their own file instead of sitting in `subagent_http.go`
  and being called from `http.go`. No behavior change.

### 0.3.0 — 2026-04-10

- **Phase 2 lands.** Four new backend types: `subagent-cli`,
  `subagent-http`, `subprocess` (ripgrep-style search), and generic
  `http`. Each is wired into `buildDispatcher` and unit-tested.
- **`pf_fault`** — the real page fault. Spawns a subagent to do a
  natural-language retrieval over configured memory, with per-call
  timeouts, partial-result capture, and a structured `timed_out` flag
  instead of an error on deadline.
- **`pf_ps`** — lists configured subagents ps-style (id, description,
  host backend).
- **New CLI subcommands.** `pagefault fault <query…> [--agent] [--timeout]`
  and `pagefault ps` — same dispatcher, same audit/filter path as the
  HTTP and MCP transports.

### 0.2.0 — 2026-04-10

- **Tool rename (breaking).** Wire surface now uses `pf_*` names:
  `list_contexts → pf_maps`, `get_context → pf_load`, `search → pf_scan`,
  `read → pf_peek` (plus `pf_fault` / `pf_ps` / `pf_poke` for Phases 2/4).
- **CLI subcommands.** `pagefault maps` / `load` / `scan` / `peek` drive the
  same dispatchers as the HTTP/MCP transports. Common flags: `--config`,
  `--no-filter`, `--json`. Config lookup: `--config → $PAGEFAULT_CONFIG →
  ./pagefault.yaml`.
- **`pf_load` skipped-source visibility.** Sources dropped by filters or
  backend errors are returned in `skipped_sources` with a reason, and
  logged at WARN. UTF-8 truncation now walks back to a rune boundary.

### 0.1.0 — 2026-04-10

- Initial Phase 1 MVP: filesystem backend, bearer auth, path/tag filters,
  audit logging, four tools (`list_contexts`, `get_context`, `search`,
  `read`) over both MCP and REST transports
- CLI: `serve`, `token create/ls/revoke`, `--version`
- Minimal config and demo data, end-to-end smoke script

See [`CHANGELOG.md`](CHANGELOG.md) for the full history.

## License

(Not yet decided. Private code until then.)
