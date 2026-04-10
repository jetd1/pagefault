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
from a directory, with four tools (`list_contexts`, `get_context`, `search`,
`read`), bearer-token auth, path/tag filters, and JSONL audit logging.

## Quick start

```bash
# Build
make build

# Run with the bundled minimal config (no auth, demo data)
./bin/pagefault serve --config configs/minimal.yaml

# In another terminal
curl -s http://127.0.0.1:8444/health | jq
curl -s -X POST http://127.0.0.1:8444/api/list_contexts | jq
curl -s -X POST http://127.0.0.1:8444/api/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"pagefault"}' | jq
curl -s -X POST http://127.0.0.1:8444/api/read \
  -H 'Content-Type: application/json' \
  -d '{"uri":"memory://README.md"}' | jq
```

The MCP endpoint is available at `POST /mcp` (streamable-http transport).

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

### 0.1.0 — 2026-04-10

- Initial Phase 1 MVP: filesystem backend, bearer auth, path/tag filters,
  audit logging, four tools (`list_contexts`, `get_context`, `search`,
  `read`) over both MCP and REST transports
- CLI: `serve`, `token create/ls/revoke`, `--version`
- Minimal config and demo data, end-to-end smoke script

See [`CHANGELOG.md`](CHANGELOG.md) for the full history.

## License

(Not yet decided. Private code until then.)
