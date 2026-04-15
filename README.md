<h1 align="center">pagefault</h1>

<p align="center">
  <em>When your agent hits a context miss, pagefault loads the right page back in.</em>
</p>

<p align="center">
  <code>v0.11.4</code>
  ·
  <a href="https://jetd1.github.io/pagefault/"><strong>live preview</strong> ↗</a>
  ·
  <a href="docs/api-doc.md">api</a>
  ·
  <a href="docs/architecture.md">architecture</a>
  ·
  <a href="docs/config-doc.md">config</a>
  ·
  <a href="docs/security.md">security</a>
  ·
  <a href="docs/design.md">design</a>
  ·
  <a href="CHANGELOG.md">changelog</a>
</p>

<p align="center">
  <sub>
    the embedded landing site is <a href="https://jetd1.github.io/pagefault/">auto-deployed to GitHub Pages</a>
    on every push to <code>main</code> via <a href=".github/workflows/pages.yml"><code>.github/workflows/pages.yml</code></a>,
    with the <code>{{version}}</code> sentinel rewritten against the current <code>VERSION</code> at build time
  </sub>
</p>

---

**pagefault** is a config-driven **memory server for AI agents**. Point it at a directory,
hand the URL to Claude Code or ChatGPT, and your agent has a memory it didn't have five
minutes ago. It takes its name from the OS primitive: when a process touches memory that
isn't resident, the kernel's fault handler quietly fetches the right page from backing
store and execution resumes. pagefault does the same for LLM agents over MCP, REST, and
the CLI.

```
   agent needs a page
            │
            ▼
      ┌───────────┐                ┌──────────────────────┐
      │   FAULT   │  ─── handler ─▶│  backends            │
      │ context   │                │   filesystem         │
      │   miss    │                │   subprocess / http  │
      └───────────┘                │   subagent-cli / http│
            ▲                      └──────────┬───────────┘
            │                                 │
         resumed ◀─────────── resolved ───────┘
            (the right page in context)
```

## Contents

- [At a glance](#at-a-glance)
- [Quick start](#quick-start)
- [Tools](#tools)
- [Transports](#transports)
- [Clients](#clients)
  - [Claude Code](#claude-code)
  - [Claude Desktop](#claude-desktop)
  - [ChatGPT Custom GPT](#chatgpt-custom-gpt-actions)
- [Production config](#production-config)
- [Development](#development)
- [Documentation](#documentation)
- [Recent changes](#recent-changes)
- [License](#license)

## At a glance

| | |
|---|---|
| **Tools**          | 7 — `pf_maps`, `pf_load`, `pf_scan`, `pf_peek`, `pf_fault`, `pf_ps`, `pf_poke` |
| **Transports**     | MCP streamable-http · MCP legacy SSE · REST · CLI                              |
| **Backends**       | `filesystem` · `subprocess` · `http` · `subagent-cli` · `subagent-http`        |
| **Auth modes**     | `none` · `bearer` · `trusted_header` · `oauth2`                                |
| **OAuth2 grants**  | `client_credentials` · `authorization_code` + PKCE · RFC 7591 DCR              |
| **Filters**        | path globs · tag allow-list · content redaction · write-path sandbox           |
| **Observability**  | JSONL audit logging · live OpenAPI 3.1 spec · per-backend health probes        |
| **Runtime**        | single Go binary, no external services, YAML config                            |
| **Landing site**   | embedded HTML/CSS/JS served at `/` by the binary + auto-deployed to [GitHub Pages](https://jetd1.github.io/pagefault/) — see [`docs/design.md`](docs/design.md) |

## Quick start

From clone to first request in under a minute. No runtime dependencies.

```bash
# 1. Build
make build
./bin/pagefault --version

# 2. Drive tools directly from the CLI — no server needed
./bin/pagefault maps                      --config configs/minimal.yaml
./bin/pagefault scan pagefault            --config configs/minimal.yaml
./bin/pagefault peek memory://README.md   --config configs/minimal.yaml
./bin/pagefault load demo                 --config configs/minimal.yaml

# 3. Or run the server and hit it over HTTP
./bin/pagefault serve --config configs/minimal.yaml
```

```bash
# (in another terminal)
curl -s http://127.0.0.1:8444/health | jq
curl -s -X POST http://127.0.0.1:8444/api/pf_maps | jq
curl -s -X POST http://127.0.0.1:8444/api/pf_scan \
  -H 'Content-Type: application/json' \
  -d '{"query":"pagefault"}' | jq

# 4. Or open the embedded landing page in a browser
open http://127.0.0.1:8444/          # macOS
xdg-open http://127.0.0.1:8444/      # Linux
```

## Tools

All seven tools are exposed over MCP, REST, and the CLI with identical filter and audit
semantics. The wire names (`pf_*`) are used for MCP registration, REST routes
(`/api/pf_*`), YAML config toggles, and audit-log entries; the CLI drops the `pf_`
prefix because the outer binary already namespaces the verbs.

| Wire         | CLI      | Does                                                      | Unix analog         |
|--------------|----------|-----------------------------------------------------------|---------------------|
| `pf_maps`    | `maps`   | List available contexts and their resources              | `/proc/self/maps`   |
| `pf_load`    | `load`   | Load a named, pre-composed context bundle                 | `mmap`              |
| `pf_scan`    | `scan`   | Search across one or every backend                       | `grep -r`           |
| `pf_peek`    | `peek`   | Read a single resource by `memory://` URI                 | `cat`               |
| `pf_fault`   | `fault`  | Spawn a subagent for deep retrieval (async by default)    | page-fault handler  |
| `pf_ps`      | `ps`     | List configured agents, or poll a task by `task_id`       | `ps`                |
| `pf_poke`    | `poke`   | Write back through the sandboxed append path              | `write(2)`          |

See [`docs/api-doc.md`](docs/api-doc.md) for the full reference (arguments, return
shapes, CLI flags, error codes).

## Transports

| Transport            | Route                          | Primary use                                     |
|----------------------|--------------------------------|-------------------------------------------------|
| MCP streamable-http  | `POST /mcp`                    | Claude Code and most programmatic MCP clients   |
| MCP legacy SSE       | `GET /sse` + `POST /message`   | Claude Desktop and other SSE-only clients       |
| REST (OpenAPI 3.1)   | `POST /api/pf_*`               | ChatGPT Custom GPT actions, ad-hoc `curl`       |
| CLI                  | `./bin/pagefault <verb>`       | Scripts, pipelines, human testing               |

Both MCP transports share the same tool set, auth chain, and instructions — pick the one
your client speaks. The SSE stream is protected against idle proxy timeouts by a
keepalive ping (0.6.1+). The landing page at `/` gives a visual summary with live
version.

## Clients

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

Claude Desktop speaks MCP over legacy SSE, not streamable-http — pagefault mounts both
transports in parallel so it works out of the box. There are three auth paths:

> **Recommended:** **(A)** native SSE with authorization code + PKCE.
> **(B)** and **(C)** are collapsed below for reference.

#### (A) Recommended — authorization code + PKCE *(0.8.0+)*

Switch the server to OAuth2 mode and register a public client:

```yaml
# pagefault.yaml
auth:
  mode: "oauth2"
  oauth2:
    clients_file: "./oauth-clients.jsonl"
  bearer:                          # optional: keep legacy bearer tokens
    tokens_file: "./tokens.jsonl"  # alive as a fallback
```

```bash
./bin/pagefault oauth-client create \
  --label "Claude Desktop" \
  --public \
  --redirect-uris "http://localhost:3000/callback" \
  --config ./pagefault.yaml
# prints:
#   id:             claude-desktop
#   label:          Claude Desktop
#   scopes:         mcp
#   redirect_uris:  http://localhost:3000/callback
#   type:           public (PKCE-only, no client_secret)
```

In Claude Desktop's MCP SSE configuration, point the server URL at
`https://pagefault.example.com/sse` and paste `claude-desktop` into the **Client ID**
field. Leave **Client Secret** empty. On connect, Claude Desktop:

1. Fetches `/.well-known/oauth-authorization-server` and discovers the
   `authorization_endpoint`.
2. Generates a PKCE code verifier + challenge.
3. Opens `/oauth/authorize?...` in your browser (pagefault auto-approves and redirects
   back immediately).
4. Exchanges the authorization code for an access token via `POST /oauth/token`.
5. Sends the token as a bearer on every subsequent request.

OAuth2 mode runs as a **compound provider** — bearer tokens in `tokens.jsonl` continue to
work alongside OAuth2, so existing Claude Code deployments need no config change, only
Claude Desktop needs the new client record.

<details>
<summary><strong>(B) Fallback — <code>client_credentials</code> grant</strong> <em>(0.7.0+)</em></summary>

<br>

For programmatic clients that prefer a static `client_id` + `client_secret` exchange,
register a confidential client instead (no `--public`, no redirect URIs):

```bash
./bin/pagefault oauth-client create \
  --label "Claude Desktop" \
  --config ./pagefault.yaml
```

Paste `claude-desktop` into the Client ID field and the printed `pf_cs_...` secret into
the Client Secret field. No browser tab, but only works when the client sends the
`client_credentials` grant type.

</details>

<details>
<summary><strong>(C) Legacy — <code>supergateway</code> bridge against <code>/mcp</code></strong></summary>

<br>

For deployments that have not yet enabled OAuth2, `npx supergateway` is still the way to
inject a bearer header into Claude Desktop's request chain:

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

> **Caveat.** Long-running `pf_fault` calls on this path are vulnerable to intermediate
> proxy idle timeouts (nginx default 60s, Node undici `headersTimeout` 60s, Cloudflare
> free plan 100s). The 0.6.1 SSE keepalive fix does **not** apply — the traffic flows
> through `/mcp` (streamable-http), not `/sse`. Bump your reverse proxy's read / idle
> timeout to 300s or more, or switch to path (A) or (B).

</details>

<details>
<summary><strong>History of Claude Desktop support</strong></summary>

<br>

| Version | Change                                                                                    |
|---------|-------------------------------------------------------------------------------------------|
| 0.6.0   | Native `/sse` transport added — before this, `supergateway` was the only Desktop option   |
| 0.6.1   | SSE keepalive pings so long tool calls survive proxy idle timeouts                        |
| 0.7.0   | OAuth2 `client_credentials` grant                                                         |
| 0.8.0   | OAuth 2.1 authorization code + PKCE flow — Claude Desktop authenticates natively          |
| 0.9.0   | RFC 7591 Dynamic Client Registration — remote-connector UI can self-register              |

</details>

### ChatGPT Custom GPT (Actions)

pagefault publishes a live OpenAPI 3.1 spec at `/api/openapi.json`. In the Custom GPT
editor, open **Actions → Import from URL** and paste:

```
https://pagefault.example.com/api/openapi.json
```

Under **Authentication**, choose **API Key → Bearer** and paste your `pf_*` token. Every
enabled `pf_*` tool becomes a Custom GPT Action.

## Production config

```bash
# 1. Start from a template.
#    configs/minimal.yaml — smallest runnable config (filesystem only)
#    configs/example.yaml — tour of every backend type, with inline docs
cp configs/example.yaml pagefault.yaml

# 2. Edit pagefault.yaml: enable bearer auth, point it at a tokens file,
#    wire up filters and backends. See docs/config-doc.md for every field.

# 3. Create a token for your first client device.
./bin/pagefault token create \
  --label "Claude Code" \
  --tokens-file ./tokens.jsonl

# 4. Run it.
./bin/pagefault serve --config ./pagefault.yaml
```

## Development

```bash
make build                # build ./bin/pagefault
make test                 # full test suite (with -race)
make test-verbose         # verbose test output
make cover                # coverage report → coverage.html
make lint                 # go vet + gofmt + staticcheck (if installed)
make fmt                  # format all Go files
make run                  # build and run with configs/minimal.yaml
make clean                # remove build artifacts
bash scripts/smoke.sh     # end-to-end smoke test
```

See [`CLAUDE.md`](CLAUDE.md) for the agent-oriented developer guide — directory tree,
conventions, versioning rules, and the "adding a new X" checklists.

## Documentation

| File                                              | Contents                                                                          |
|---------------------------------------------------|-----------------------------------------------------------------------------------|
| [`docs/api-doc.md`](docs/api-doc.md)              | Tool reference for all seven `pf_*` tools (HTTP + CLI)                            |
| [`docs/config-doc.md`](docs/config-doc.md)        | Full YAML configuration reference                                                 |
| [`docs/architecture.md`](docs/architecture.md)    | Architecture deep dive — request flow, backend model, transports, OAuth2 wiring   |
| [`docs/security.md`](docs/security.md)            | Threat model, auth, filters, audit logging, rate limiting, CORS, write safety     |
| [`docs/design.md`](docs/design.md)                | Design system — concept, voice, color, type, icons, spacing, motion, a11y         |
| [`CLAUDE.md`](CLAUDE.md)                          | Developer guide for AI agents working on this repo                                |
| [`CHANGELOG.md`](CHANGELOG.md)                    | Authoritative history of shipped features                                         |

## Recent changes

### 0.12.1 — 2026-04-15

- **Parallel `pf_scan` fan-out.** `ToolDispatcher.Search` now runs
  every target backend on its own goroutine and merges results in
  configured backend order, so a slow HTTP or subprocess search
  no longer blocks the faster filesystem/ripgrep backends. A
  `pf_scan` across N backends now takes roughly `max(per-backend
  latency)` instead of the sum. A failing backend emits a
  `slog.Warn` ("search: backend failed") with backend + error +
  caller id instead of a silent `continue`, so operator logs
  carry a signal when a backend is chronically unhealthy. Wire
  shape unchanged.
- **Fixed wrong example path in MCP instructions.** The default
  server-level instructions cited `memory://daily/2026-04-11.md`
  for the `pf_peek` example; real default configs lay memory out
  under `memory://memory/…`, so an agent copying the example
  verbatim would hit `resource_not_found`. Path corrected to
  match.

### 0.12.0 — 2026-04-15

- **MCP `serverInfo` branding.** The MCP `initialize` response now
  emits `title: "pagefault"`, a design-system description, a
  website URL (default: `server.public_url`), and a self-contained
  amber-on-dark SVG icon so Claude Desktop and other MCP clients
  render pagefault as a first-class connector — logomark in the
  sidebar, one-sentence description in the connector picker —
  instead of a generic `pagefault` row with a globe icon. Four
  new optional YAML fields (`server.mcp.{title, description, website_url, icon_url}`)
  let operators override the defaults for branded instances; the
  icon travels as a `data:image/svg+xml;base64,…` URI so local
  and internal deployments without a public URL still ship a
  branded logomark out of the box. Each of the seven `pf_*` tools
  also picks up a human-readable `Annotations.Title` (`List Memory
  Regions`, `Deep Memory Query`, `Write to Memory`, …), corrected
  `ReadOnlyHint` / `DestructiveHint` / `IdempotentHint` values,
  and a per-tool glyph extracted from the landing-site sprite —
  MCP clients no longer flag `pf_scan` or `pf_peek` as destructive
  by default. Implemented via an `AddAfterInitialize` hook on
  mcp-go's server (no library fork), a new `web/icon.svg` (governed
  by `docs/design.md §5.1`) mounted at `/icon.svg`, and a one-time
  parse of `web/icons.svg` into per-tool data URIs at `init` time.

### 0.11.4 — 2026-04-12

- **Mobile readability on the landing site's Quick start section.**
  Long shell commands like `./bin/pagefault peek memory://README.md
  --config configs/minimal.yaml` used to force a horizontal scroll
  on every step because `.code-block` was stuck with the browser
  default `white-space: pre`. `web/styles.css` now carries
  `white-space: pre-wrap; overflow-wrap: anywhere;` unconditionally
  so long commands wrap at the viewport edge instead of scrolling
  sideways — copy-paste still yields the original unwrapped text
  because `pre-wrap` is visual-only. The `@media (max-width: 720px)`
  block also tightens code-block padding (`16×24` → `12×16`) and
  drops the font from `--fs-sm` (14px) to `--fs-xs` (12px), shrinks
  mobile `.step__title` and `.step__marker` from `--fs-lg` to
  `--fs-md` for a lighter step rail, and drops `.step__title`'s
  bottom margin so titles sit closer to their code block. Desktop
  layout is unchanged. Reaches the live preview via 0.11.3's
  GitHub Pages auto-deploy.

See [`CHANGELOG.md`](CHANGELOG.md) for the full history.

## License

Not yet decided. Private source until then.
