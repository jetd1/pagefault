# pagefault

> When your agent hits a context miss, pagefault loads the right page back in.

**pagefault** is a config-driven memory server that exposes personal
knowledge (files, search indices, agent sessions) to external AI clients via
**MCP** and **REST**. The metaphor: in an OS a page fault occurs when a
process accesses memory that isn't resident — the handler fetches it from
backing store and resumes execution. pagefault does the same for AI agents:
when they need context they don't have, they fault to this server, which
loads the right information from configured backends.

Phases 1–4 ship a Go binary that serves markdown files from a directory,
answers search via subprocess or HTTP backends, spawns real subagents
for deep retrieval, writes back through a sandboxed append path or a
subagent, and exposes the surface over MCP, REST, and the CLI with
opt-in rate limiting, CORS, and a live OpenAPI spec. Seven tools
(`pf_maps`, `pf_load`, `pf_scan`, `pf_peek`, `pf_fault`, `pf_ps`,
`pf_poke`), bearer-token auth, path/tag/redaction filters, and JSONL
audit logging. Tool names follow a `pf_*` scheme borrowed from Unix
memory management and kernel debugging — see `docs/api-doc.md` for
the mapping.

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
- **`docs/api-doc.md`** — tool reference (Phase 1–4, all seven `pf_*` tools)
- **`docs/config-doc.md`** — full YAML configuration reference
- **`docs/architecture.md`** — architecture deep dive
- **`docs/security.md`** — threat model, auth, filters, audit, write safety
- **`CLAUDE.md`** — developer guide for AI agents working on this repo

## Recent Changes

### 0.5.1 — 2026-04-11

- **Bug fix: `max_entry_size` now enforced on raw caller content,
  not post-wrap bytes.** Before 0.5.1 the cap was measured in the
  filesystem backend *after* `write.FormatEntry` had added its
  ~40–60 byte header, silently penalising `format: "entry"` callers
  by the wrapper overhead and breaking the documented "raw and
  entry share one budget" promise. The check moved up into
  `handleWriteDirect` and runs against `len(in.Content)` before
  wrapping. No wire or config schema changes — only oversize
  edge-case writes behave differently.
- **Doc drift cleanups.** `configs/example.yaml` no longer marks
  `pf_poke` as "not yet implemented"; it enables the tool and
  ships a commented-out Phase-4 write block on the `fs` backend
  plus a write-filter example. `docs/config-doc.md` now calls out
  the `write_paths` URI-scheme footgun (patterns must include
  `memory://…`) and the `max_entry_size: 0` default-rewrite gotcha.
  `internal/write/writer.go` clarifies that `WriteModeAny`
  currently only unlocks `format: "raw"` — prepend and overwrite
  are reserved for a future phase. `plan.md`'s `pf_poke` error
  table now correctly describes agent-mode timeouts as
  `200 OK + timed_out: true` instead of `504`.
- **New known-limitation notes** in `docs/security.md`: the
  `resolveWritePath` TOCTOU window (single-operator deployments
  only; a multi-tenant fix needs `openat(O_NOFOLLOW)`), agent-mode
  writes showing up in the audit log as `tool: "pf_fault"` rather
  than `"pf_poke"`, and the response-envelope `targets_written`
  field being reserved but always absent until structured
  subagent responses ship in Phase 5.

### 0.5.0 — 2026-04-11

- **Phase 4 ships — `pf_poke`.** Write content back into memory via
  two modes: `direct` (sandboxed filesystem append) and `agent`
  (delegate the task to a subagent). Filesystem backends are
  read-only by default — set `writable: true` plus an explicit
  `write_paths` allowlist to enable direct writes. Five independent
  gates protect a direct append: tool enable flag, server-wide
  `filters.path.write_allow/deny`, backend `Writable()` flag,
  per-backend `write_paths`, and `max_entry_size`. `flock(2)`
  serialises concurrent writers.
- **New `internal/write` package.** `FilesystemWriter` takes LOCK_EX
  on the open fd and does atomic `O_APPEND|O_CREATE` writes.
  `FormatEntry` wraps `format: "entry"` payloads as
  `\n---\n## [HH:MM] via pagefault (<caller>)\n\n<content>\n`;
  `format: "raw"` appends bytes unchanged but requires
  `write_mode: "any"` on the target backend (a second-tier opt-in).
- **`model.ErrContentTooLarge` sentinel.** Mapped to HTTP 413 /
  code `content_too_large` in the structured REST error envelope.
  Oversized payloads (measured on the raw caller content before
  entry-template wrapping) get a clean rejection.
- **`pf_poke` wired across every transport.** MCP tool registered
  with full JSON schema, REST route at `/api/pf_poke`, OpenAPI
  spec gains `WriteInput` / `WriteOutput` schemas + 413 error row,
  and CLI `pagefault poke --mode direct|agent [--uri URI]
  <content...>` (with stdin fallback so `echo ... | pagefault poke
  --mode direct --uri ...` works).
- **Docs rewritten.** `docs/api-doc.md` gains the full `pf_poke`
  section, `docs/config-doc.md` documents the filesystem write
  fields + write filter layer, `docs/security.md` §Write safety is
  fully rewritten (five-gate model, entry template rationale, agent
  mode trust delegation, updated threat table + checklist),
  `docs/architecture.md` gets the write branch in the filter
  pipeline diagram and `internal/write` in the package map.

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

See [`CHANGELOG.md`](CHANGELOG.md) for the full history.

## License

(Not yet decided. Private code until then.)
