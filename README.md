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
# 1. Copy the minimal config as a starting point
cp configs/minimal.yaml pagefault.yaml

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
- **`docs/security.md`** added (threat model, filter/audit notes, known
  limitations, deployment checklist) — previously missing from the spec.

### 0.1.0 — 2026-04-10

- Initial Phase 1 MVP: filesystem backend, bearer auth, path/tag filters,
  audit logging, four tools (`list_contexts`, `get_context`, `search`,
  `read`) over both MCP and REST transports
- CLI: `serve`, `token create/ls/revoke`, `--version`
- Minimal config and demo data, end-to-end smoke script

See [`CHANGELOG.md`](CHANGELOG.md) for the full history.

## License

(Not yet decided. Private code until then.)
