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
opt-in rate limiting, CORS, and a live OpenAPI spec. 0.7.0 adds an
OAuth2 client_credentials auth provider so Claude Desktop's native
SSE configuration (Client ID + Client Secret only) works without the
`supergateway` bridge. Seven tools (`pf_maps`, `pf_load`, `pf_scan`,
`pf_peek`, `pf_fault`, `pf_ps`, `pf_poke`), bearer-token or OAuth2
auth (compound mode supported), path/tag/redaction filters, and
JSONL audit logging. Tool names follow a `pf_*` scheme borrowed from
Unix memory management and kernel debugging — see `docs/api-doc.md`
for the mapping.

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

MCP is served over two transports in parallel when `serve` is
running: streamable-http at `POST /mcp` (for Claude Code and most
programmatic clients) and legacy SSE at `GET /sse` + `POST /message`
(for Claude Desktop and other SSE-only clients). Both share the same
tool set, auth chain, and instructions — pick the one your client
speaks. The CLI form (`pagefault peek`, `pagefault scan`, …) is the
same vocabulary minus the `pf_` prefix — see `docs/api-doc.md` for the
full reference.

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

Claude Desktop speaks MCP over legacy SSE, not streamable-http.
pagefault mounts both transports in parallel (`/mcp` for
streamable-http, `/sse` + `/message` for SSE — they share the same
tool set, auth chain, and instructions), and as of 0.7.0 also
ships an OAuth2 client_credentials auth provider so Claude Desktop
can connect natively without a local bridge.

Claude Desktop's SSE config UI only exposes two extra credential
fields: **Client ID** and **Client Secret**. It does not accept a
plain `Authorization: Bearer pf_...` header. There are two paths,
depending on how pagefault is authenticated:

**(A) Recommended (0.7.0+): native SSE with OAuth2.** Switch the
server to OAuth2 mode and register a Claude Desktop client:

```yaml
# pagefault.yaml
auth:
  mode: "oauth2"
  oauth2:
    clients_file: "./oauth-clients.jsonl"
  bearer:                         # optional: keep legacy bearer
    tokens_file: "./tokens.jsonl" # tokens alive as a fallback
```

```bash
./bin/pagefault oauth-client create \
  --label "Claude Desktop" \
  --config ./pagefault.yaml
# prints:
#   id:     claude-desktop
#   label:  Claude Desktop
#   scopes: mcp
#   secret: pf_cs_XXXXXXXXXXXXXXXXXXXXXXXX
#
# Record this secret now — it will not be shown again.
```

Then, in Claude Desktop's MCP SSE configuration, set the server
URL to `https://pagefault.example.com/sse`, paste `claude-desktop`
into the Client ID field, and paste `pf_cs_...` into the Client
Secret field. Claude Desktop will hit `POST /oauth/token` with
those credentials, receive a short-lived access token, and use it
as a bearer on every subsequent request. The 0.6.1 SSE keepalive
fix protects the long-lived `/sse` stream against idle proxy
timeouts, so long `pf_fault` calls survive cleanly.

Because `auth.mode: "oauth2"` runs in **compound mode**, any
bearer tokens you previously created in `tokens.jsonl` continue
to work alongside OAuth2 — Claude Code deployments keep their
existing config and only Claude Desktop needs the new client
record.

**(B) Fallback: the `supergateway` bridge against `/mcp`.** For
deployments that have not (yet) enabled OAuth2, `npx supergateway`
is still the way to inject a bearer header into Claude Desktop's
request chain:

```json
{
  "mcpServers": {
    "pagefault": {
      "command": "npx",
      "args": [
        "-y", "supergateway",
        "--streamableHttp", "https://pagefault.example.com/mcp",
        "--header", "Authorization: Bearer pf_your_token_here"
      ]
    }
  }
}
```

**Important caveat for (B):** long-running `pf_fault` calls on
this path are vulnerable to intermediate proxy idle timeouts
(nginx default 60s, Node undici `headersTimeout` 60s, Cloudflare
free plan 100s). The 0.6.1 SSE keepalive fix does *not* help
here because the traffic flows through `/mcp` (streamable-http),
not `/sse`. If you run a reverse proxy in front of pagefault and
use path (B), bump its read / idle timeout to 300s or more. Path
(A) does not have this issue.

> **History.** Before 0.6.0 pagefault only shipped the
> streamable-http transport, so `supergateway` was literally
> the only way to connect Claude Desktop at all. 0.6.0 added
> the native `/sse` transport; 0.6.1 added SSE keepalive pings
> that make long tool calls survive proxy timeouts; 0.7.0
> added the OAuth2 auth provider that makes Claude Desktop's
> native SSE config reachable without a local bridge.

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

### 0.7.1 — 2026-04-11

- **OAuth2 review-pass hardening.** External review of 0.7.0
  flagged three issues; this release fixes the two actionable
  ones. **(1) File-based revocation now cuts active sessions.**
  `OAuth2Provider.ReloadClients` sweeps any issued access tokens
  whose owning client has disappeared from the reloaded file,
  so the sequence `pagefault oauth-client revoke → rewrite JSONL
  → reload` fully invalidates an already-authenticated client in
  one step instead of silently keeping the stale tokens valid
  until their TTL. A new `OAuth2Provider.RevokeClient(clientID)`
  method does the same in-memory purge directly — exposed now so
  a future SIGHUP reload handler or admin endpoint can force
  immediate invalidation without a full restart. The CLI still
  runs out-of-process and can't reach the server's token store
  today, so `oauth-client revoke`'s output has been rewritten to
  spell that gap out explicitly. **(2) `POST /oauth/token` is
  now strict about `grant_type` placement.** The previous
  lenient fallback that accepted `grant_type` from the URL
  query string has been removed — the field is now read from
  `r.PostForm` only, per RFC 6749 §4.4's requirement that it
  arrive in the application/x-www-form-urlencoded body. A
  non-compliant client gets `unsupported_grant_type` so the
  bug is visible in its logs at integration time instead of
  silently succeeding.

### 0.7.0 — 2026-04-11

- **OAuth2 client_credentials auth provider.** Shipped to unblock
  Claude Desktop's native SSE MCP configuration, which as of
  2026-04 only accepts **Client ID / Client Secret** in its
  credential UI. New `auth.mode: "oauth2"` runs a full RFC 6749
  §4.4 client_credentials grant: the three standard endpoints
  (`GET /.well-known/oauth-protected-resource` per RFC 9728,
  `GET /.well-known/oauth-authorization-server` per RFC 8414, and
  `POST /oauth/token` for the grant itself) are mounted as
  **public** so MCP clients can bootstrap before they have a
  token. Opaque access tokens are issued with a configurable TTL
  (default 3600s), held in an in-memory store with lazy expiry,
  and scoped by intersection of the client's allowed scopes and
  the caller-requested set. Clients are registered out-of-band
  via a new `pagefault oauth-client create` CLI subcommand that
  mirrors `pagefault token`; the client secret is printed exactly
  once at creation time and stored only as a bcrypt hash
  afterwards. The provider also runs as a **compound** mode —
  when `auth.bearer.tokens_file` is populated alongside
  `auth.oauth2.clients_file`, long-lived static bearer tokens
  keep working as a fallback, so operators can move Claude
  Desktop to OAuth2 without breaking Claude Code deployments on
  the same server. The Claude Desktop connect section below has
  been rewritten to lead with the native OAuth2 path and keep
  `supergateway` only as a fallback for bearer-only deployments.

### 0.6.1 — 2026-04-11

- **Hotfix: SSE keepalive pings enabled by default.** A real
  Claude Desktop deployment reported `pf_fault` calls dying after
  "几十秒" (a few tens of seconds) regardless of the configured
  `timeout_seconds`, well before the subagent finished. Root
  cause: pagefault's internal code respects the full timeout
  end-to-end, but mcp-go's SSE server does not enable keepalives
  by default, so the persistent GET `/sse` stream sat idle for
  the whole subagent wait and whichever intermediate proxy
  timeout fired first (nginx `proxy_read_timeout` 60s, Node
  undici `headersTimeout` 60s, Cloudflare free plan 100s, …)
  closed the connection mid-call. Fix: pass
  `WithKeepAlive(true)` + `WithKeepAliveInterval(15s)` when
  mounting the SSE server, so pagefault emits a JSON-RPC `ping`
  event on the stream every 15 seconds. Two new opt-out YAML
  fields — `server.mcp.sse_keepalive` and
  `server.mcp.sse_keepalive_interval_seconds` — let operators
  tune or disable the feature. Operators using
  `supergateway --streamableHttp → /mcp` are **not** helped by
  this fix (mcp-go's streamable-http transport has no equivalent
  per-request keepalive). Native `/sse` on Claude Desktop is
  *only* an option for OAuth2-authenticated deployments as of
  2026-04 — Claude Desktop's built-in SSE config does not accept
  a plain bearer header, so bearer-auth users still need the
  `supergateway` bridge or a reverse-proxy workaround until
  pagefault ships an OAuth2 auth provider (tracked for Phase 5).
  Other SSE clients benefit from the keepalive fix directly.

See [`CHANGELOG.md`](CHANGELOG.md) for the full history.

## License

(Not yet decided. Private code until then.)
