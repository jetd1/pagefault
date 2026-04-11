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
tool set, auth chain, and instructions). Claude Desktop uses the
MCP-standard **OAuth 2.1 authorization code + PKCE flow** to
authenticate: it opens a browser tab, you approve, and it receives
a short-lived access token. pagefault 0.8.0 supports this flow
natively — no bridge required.

**(A) Recommended (0.8.0+): native SSE with authorization code + PKCE.**

1. Switch the server to OAuth2 mode and register a client with
   redirect URIs for Claude Desktop's local callback:

```yaml
# pagefault.yaml
auth:
  mode: "oauth2"
  oauth2:
    clients_file: "./oauth-clients.jsonl"
  bearer:                         # optional: keep legacy bearer
    tokens_file: "./tokens.jsonl" # tokens alive as a fallback
```

2. Register a client. Claude Desktop is a **public client** (it
   uses PKCE instead of a client_secret), so pass `--public`:

```bash
./bin/pagefault oauth-client create \
  --label "Claude Desktop" \
  --public \
  --redirect-uris "http://localhost:3000/callback" \
  --config ./pagefault.yaml
# prints:
#   id:     claude-desktop
#   label:  Claude Desktop
#   scopes: mcp
#   redirect_uris: http://localhost:3000/callback
#   type:   public (PKCE-only, no client_secret)
#
# Use the id as the OAuth2 Client ID in your client configuration.
# This is a public client — no client_secret is needed; PKCE protects the flow.
```

3. In Claude Desktop's MCP SSE configuration, set the server URL
   to `https://pagefault.example.com/sse` and paste
   `claude-desktop` into the **Client ID** field. Leave the
   **Client Secret** field empty. When you click connect, Claude
   Desktop will:

   - Fetch `/.well-known/oauth-authorization-server` and discover
     the `authorization_endpoint`
   - Generate a PKCE code verifier + challenge
   - Open your browser to `/oauth/authorize?...` (pagefault
     auto-approves and redirects back immediately)
   - Exchange the authorization code for an access token via
     `POST /oauth/token` with PKCE
   - Use the token as a bearer on every subsequent request

The 0.6.1 SSE keepalive fix protects the long-lived `/sse` stream
against idle proxy timeouts, so long `pf_fault` calls survive
cleanly.

Because `auth.mode: "oauth2"` runs in **compound mode**, any
bearer tokens you previously created in `tokens.jsonl` continue
to work alongside OAuth2 — Claude Code deployments keep their
existing config and only Claude Desktop needs the new client
record.

**(B) Fallback: client_credentials grant.** If you prefer the
older client_credentials flow (where Claude Desktop exchanges a
static client_id + client_secret for a token without opening a
browser), register a confidential client instead:

```bash
./bin/pagefault oauth-client create \
  --label "Claude Desktop" \
  --config ./pagefault.yaml
```

Then paste `claude-desktop` into the Client ID field and the
printed `pf_cs_...` secret into the Client Secret field. This
path does not open a browser tab but only works when Claude
Desktop sends the `client_credentials` grant type.

**(C) Legacy: the `supergateway` bridge against `/mcp`.** For
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

**Important caveat for (C):** long-running `pf_fault` calls on
this path are vulnerable to intermediate proxy idle timeouts
(nginx default 60s, Node undici `headersTimeout` 60s, Cloudflare
free plan 100s). The 0.6.1 SSE keepalive fix does *not* help
here because the traffic flows through `/mcp` (streamable-http),
not `/sse`. If you run a reverse proxy in front of pagefault and
use path (C), bump its read / idle timeout to 300s or more. Paths
(A) and (B) do not have this issue.

> **History.** Before 0.6.0 pagefault only shipped the
> streamable-http transport, so `supergateway` was literally
> the only way to connect Claude Desktop at all. 0.6.0 added
> the native `/sse` transport; 0.6.1 added SSE keepalive pings
> that make long tool calls survive proxy timeouts; 0.7.0
> added the OAuth2 client_credentials auth provider; 0.8.0
> added the authorization code + PKCE flow that Claude Desktop
> uses natively.

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

### 0.8.0 — 2026-04-11

- **OAuth2 authorization code + PKCE flow.** Claude Desktop
  uses the MCP-standard browser-based OAuth 2.1 flow with PKCE,
  not the client_credentials grant. pagefault now supports the
  full authorization_code grant alongside client_credentials. New
  endpoints: `GET /oauth/authorize` (auto-approves by default),
  `POST /oauth/authorize` (consent form), and `POST /oauth/token`
  extended for `grant_type=authorization_code`. Public clients
  (no client_secret) authenticate via PKCE alone. The RFC 8414
  metadata now advertises `authorization_endpoint`,
  `code_challenge_methods_supported: ["S256"]`, and both grant
  types. Operators register clients with `--redirect-uris` and
  optionally `--public` for PKCE-only clients. No dynamic client
  registration (DCR) endpoint — clients are pre-registered via
  CLI for security on internet-facing deployments.

### 0.7.1 — 2026-04-11

- **OAuth2 review-pass hardening.** See CHANGELOG.md for details.

### 0.7.0 — 2026-04-11

- **OAuth2 client_credentials auth provider.** See CHANGELOG.md for details.

See [`CHANGELOG.md`](CHANGELOG.md) for the full history.

## License

(Not yet decided. Private code until then.)
