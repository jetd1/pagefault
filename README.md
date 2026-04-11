# pagefault

> When your agent hits a context miss, pagefault loads the right page back in.

**pagefault** is a config-driven memory server that exposes personal
knowledge (files, search indices, agent sessions) to external AI clients via
**MCP** and **REST**. The metaphor: in an OS a page fault occurs when a
process accesses memory that isn't resident — the handler fetches it from
backing store and resumes execution. pagefault does the same for AI agents:
when they need context they don't have, they fault to this server, which
loads the right information from configured backends.

pagefault ships a Go binary that serves markdown files from a
directory, answers search via subprocess or HTTP backends, spawns
real subagents for deep retrieval, writes back through a sandboxed
append path or a subagent, and exposes the surface over MCP
(streamable-http **and** legacy SSE), REST, and the CLI with opt-in
rate limiting, CORS, and a live OpenAPI spec. 0.8.0 added the
MCP-standard **OAuth 2.1 authorization code + PKCE flow** so Claude
Desktop (and any other browser-based MCP client) can authenticate
natively without the `supergateway` bridge; 0.9.0 layered **RFC
7591 Dynamic Client Registration** on top so the remote-connector
UI can self-register a public client; 0.10.0 reshapes `pf_fault`
into an **async task model** — the call returns a `task_id`
immediately, the subagent runs on a detached goroutine, and the
caller polls `pf_ps(task_id=...)` (30s × 6, ~3 minutes) for the
result — so HTTP disconnects and proxy timeouts no longer kill
long retrieval runs. 0.7.0's client_credentials grant remains
available as a fallback for programmatic clients. Seven tools
(`pf_maps`, `pf_load`, `pf_scan`, `pf_peek`, `pf_fault`, `pf_ps`,
`pf_poke`), four auth modes (`none` / `bearer` / `trusted_header`
/ `oauth2`, the last running compound with `bearer` for
migration), path/tag/redaction filters, and JSONL audit logging.
Tool names follow a `pf_*` scheme borrowed from Unix memory
management and kernel debugging — see `docs/api-doc.md` for the
mapping.

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

- **`docs/api-doc.md`** — tool reference for all seven `pf_*` tools (HTTP + CLI)
- **`docs/config-doc.md`** — full YAML configuration reference
- **`docs/architecture.md`** — architecture deep dive
- **`docs/security.md`** — threat model, auth, filters, audit, write safety
- **`CLAUDE.md`** — developer guide for AI agents working on this repo
- **`CHANGELOG.md`** — authoritative history of shipped features

## Recent Changes

### 0.11.1 — 2026-04-12

- **Two 0.11.0 follow-up fixes.** (1) The landing page's version
  badge no longer drifts from `VERSION` — `web/index.html` now
  carries a `{{version}}` sentinel in the three places where
  the version string appears (nav badge, footer badge,
  quickstart `pagefault --version` snippet), and
  `internal/server.New` substitutes it at startup against the
  injected `Version` variable, serving the rendered bytes
  through `http.ServeContent` (so Content-Type, Content-Length,
  Last-Modified, HEAD, and Range handling all stay correct).
  0.11.0 shipped with `v0.10.1` literally hardcoded in the HTML —
  cosmetic but embarrassing; this fix makes the class of bug
  impossible on future bumps. (2) The Go module path has been
  renamed from `github.com/jet/pagefault` to the vanity path
  `jetd.one/pagefault`. `go.mod` plus every `import` statement
  (144 occurrences across 51 Go files) has been rewritten; no
  runtime behaviour changes, and the GitHub hosting URL
  (`github.com/jetd1/pagefault`) is unchanged. Module path and
  hosting URL are now correctly treated as two independent
  identifiers, closing the confusion that caused 0.11.0's
  initial href bug.

### 0.11.0 — 2026-04-12

- **Landing site + design system.** `pagefault serve` now
  answers `GET /` with a proper HTML landing page instead of
  the plain-text endpoint dump — hero with an animated
  `pf_fault` terminal, concept, seven-tool table with inline
  glyph icons, four-step quickstart, transports, and an ASCII
  architecture diagram. The static site lives in `web/` as pure
  HTML / CSS / JS / SVG (no build step) and is embedded into
  the binary via `//go:embed`, served through
  `http.FileServerFS` with explicit GET + HEAD routes for each
  of the five asset paths (`/`, `/styles.css`, `/script.js`,
  `/favicon.svg`, `/icons.svg`) so it never shadows `/api/*`,
  `/mcp`, `/sse`, or the OAuth2 surface. Governed by a new
  `docs/design.md` — eleven sections covering concept, voice,
  color tokens, typography, iconography, spacing, motion,
  accessibility, and error-state vocabulary — whose semantic
  palette maps directly onto the `task.Status` enum so HTML,
  CLI, and HTTP error envelopes all speak the same color
  language. The `handleRoot` plain-text endpoint list has been
  removed; the obsolete `TestServer_SSE_Disabled_RootLandingHidesIt`
  test (which asserted the old dynamic endpoint list) is
  deleted, replaced with `TestServer_Root_ServesEmbeddedLanding`
  and `TestServer_StaticAssets_Served`.

### 0.10.1 — 2026-04-11

- **Three regressions from 0.10.0 fixed.** (1) A panic in any
  backend `Spawn` method no longer crashes the entire server — the
  task manager's detached goroutine now recovers panics and
  converts them into `StatusFailed` tasks (before, the goroutine
  was outside `net/http`'s panic-recovery reach and an
  unrecovered panic killed the whole `pagefault` binary). (2)
  `pf_poke` mode:agent no longer reports failed subagent writes as
  `{status:"written", result:""}` — the handler now inspects the
  task snapshot's `Status` field and returns
  `ErrBackendUnavailable` for `failed` / non-terminal tasks
  instead of silently losing the content. (3) `pf_poke` mode:agent
  surfaces caller-cancel-during-Wait as an error rather than a
  false success. Plus three low-severity nits around the
  `task.Manager.Wait` TTL-sweep race, task-manager cleanup on
  `dispatcher.New` failure, and audit-log error wrapping for
  `GenerateSpawnID`. Regression tests added under `-race`.

See [`CHANGELOG.md`](CHANGELOG.md) for the full history.

## License

(Not yet decided. Private code until then.)
