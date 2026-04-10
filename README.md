# pagefault

> When your agent hits a context miss, pagefault loads the right page back in.

**pagefault** is a config-driven memory server that exposes personal
knowledge (files, search indices, agent sessions) to external AI clients via
**MCP** and **REST**. The metaphor: in an OS a page fault occurs when a
process accesses memory that isn't resident — the handler fetches it from
backing store and resumes execution. pagefault does the same for AI agents:
when they need context they don't have, they fault to this server, which
loads the right information from configured backends.

Phases 1–3 ship a Go binary that serves markdown files from a directory,
answers search via subprocess or HTTP backends, spawns real subagents for
deep retrieval, and exposes the surface over MCP, REST, and the CLI with
opt-in rate limiting, CORS, and a live OpenAPI spec. Six tools
(`pf_maps`, `pf_load`, `pf_scan`, `pf_peek`, `pf_fault`, `pf_ps`),
bearer-token auth, path/tag/redaction filters, and JSONL audit logging.
Tool names follow a `pf_*` scheme borrowed from Unix memory management
and kernel debugging — see `docs/api-doc.md` for the mapping.

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

## Connect a client

Once `pagefault serve` is running behind TLS (e.g. `https://pagefault.example.com`),
pointing an AI client at it is one or two commands.

### Claude Code

```bash
claude mcp add pagefault \
  --transport http \
  --url https://pagefault.example.com/mcp \
  --header "Authorization: Bearer pf_your_token_here"
```

Restart Claude Code; the `pf_*` tools appear alongside the built-ins.

### Claude Desktop

Add an entry to your Claude Desktop MCP server config (the file path
depends on your platform — see the Claude Desktop docs):

```json
{
  "mcpServers": {
    "pagefault": {
      "transport": { "type": "http", "url": "https://pagefault.example.com/mcp" },
      "headers":   { "Authorization": "Bearer pf_your_token_here" }
    }
  }
}
```

### ChatGPT Custom GPT (Actions)

pagefault publishes a live OpenAPI spec at `/api/openapi.json`. In the
Custom GPT editor, open **Actions → Import from URL** and paste:

```
https://pagefault.example.com/api/openapi.json
```

Then, under **Authentication**, choose **API Key → Bearer** and paste
your `pf_...` token. The GPT can now call every enabled `pf_*` tool as
an Action.

## Tests and linting

```bash
make test         # full test suite with race detector
make lint         # go vet + gofmt check
make cover        # coverage report (coverage.html)
bash scripts/smoke.sh   # end-to-end smoke test
```

## Documentation

- **`plan.md`** — full product spec and roadmap (source of truth)
- **`docs/api-doc.md`** — tool reference (Phase 1-2)
- **`docs/config-doc.md`** — full YAML configuration reference
- **`docs/architecture.md`** — architecture deep dive
- **`CLAUDE.md`** — developer guide for AI agents working on this repo

## Recent Changes

### 0.4.1 — 2026-04-11

- **`pf_load` response now reports the actual format.** Previously,
  leaving `format` empty in the request made the handler echo back a
  hard-coded `"markdown"` even when the context's configured default
  was `json` or `markdown-with-metadata` — clients that branched on
  `format` were misled. The dispatcher now returns the resolved format
  and `HandleGetContext` echoes it.
- **CORS preflight no longer short-circuits disallowed origins.** An
  `OPTIONS + Access-Control-Request-Method` from an origin outside the
  allowlist used to return 204 + no headers, bypassing the downstream
  auth / route chain. The shortcut is now gated on `originAllowed`.
- **`rate_limited` is a first-class sentinel error.**
  `model.ErrRateLimited` routes through the shared `writeError` plumbing
  so the rate-limit middleware no longer hand-rolls an envelope literal.
- Minor: the root landing page lists `/api/openapi.json`; dead
  `lastSeen` field dropped from `rateLimiter` with a comment explaining
  why GC is deliberately not implemented yet; MCP `pf_load` description
  now mentions `markdown-with-metadata`.

### 0.4.0 — 2026-04-11

- **Phase 3 ships.** Live `RedactionFilter` (regex content masking with
  capture groups), JSON and `markdown-with-metadata` context formats
  for `pf_load`, public `/api/openapi.json` (OpenAPI 3.1.0 generated
  from the live config), opt-in `server.cors`, per-caller in-process
  rate limiting via `server.rate_limit`, and richer `/health` output
  (`ok` / `degraded` / `unavailable` plus per-backend error strings
  via the new `HealthChecker` interface).
- **Structured error envelope.** Every REST error is now
  `{"error":{"code":"invalid_request","status":400,"message":"..."}}`
  with a stable snake_case `code` field (`rate_limited`,
  `backend_unavailable`, `agent_not_found`, etc.). **Breaking** for
  REST clients that parsed the old `{"error","message"}` shape; MCP
  clients are unaffected.
- **README client setup guides** for Claude Code, Claude Desktop, and
  ChatGPT Custom GPT Actions.

### 0.3.2 — 2026-04-10

- **HTTP backend no longer masks operator typos.** A configured
  `response_path` that isn't present in the response body used to
  silently return zero results. It now surfaces a wrapped
  `ErrBackendUnavailable` (→ HTTP 502) naming the missing path, so the
  caller sees "response path \"results\" not found in response body"
  instead of empty search results.
- **`hasAgent` deduplicated** across `subagent_cli.go` and
  `subagent_http.go` into a single `hasAgentID(agents, id)` helper in
  `subagent.go`. Behaviour unchanged.
- **Doc cleanup.** README / CLAUDE.md / api-doc.md references to
  "Phase 1 / four tools / 0.3.0" updated to reflect the current
  Phase-1-2 / six-tool / 0.3.2 reality.

See [`CHANGELOG.md`](CHANGELOG.md) for the full history.

## License

(Not yet decided. Private code until then.)
