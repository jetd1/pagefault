// Package config defines the pagefault YAML configuration schema and provides
// loading, environment-variable substitution, and validation.
//
// The server is a pure runtime for a Config value — every behavior (backends,
// contexts, tools, filters, auth, audit) is driven from a YAML file.
//
// A Config is loaded via Load, which reads the file, expands ${ENV_VAR}
// references using os.ExpandEnv, unmarshals it into a Config struct, applies
// defaults, and validates the result with go-playground/validator.
package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

// Config is the root of the pagefault configuration.
type Config struct {
	Server   ServerConfig    `yaml:"server" validate:"required"`
	Auth     AuthConfig      `yaml:"auth" validate:"required"`
	Backends []BackendConfig `yaml:"backends" validate:"required,min=1,dive"`
	Contexts []ContextConfig `yaml:"contexts" validate:"dive"`
	Tools    ToolsConfig     `yaml:"tools"`
	Filters  FiltersConfig   `yaml:"filters"`
	Audit    AuditConfig     `yaml:"audit"`
}

// ServerConfig configures the HTTP listener.
type ServerConfig struct {
	Host      string          `yaml:"host" validate:"required"`
	Port      int             `yaml:"port" validate:"required,gt=0,lt=65536"`
	PublicURL string          `yaml:"public_url,omitempty"`
	CORS      CORSConfig      `yaml:"cors,omitempty"`
	RateLimit RateLimitConfig `yaml:"rate_limit,omitempty"`
	MCP       MCPConfig       `yaml:"mcp,omitempty"`
	Tasks     TasksConfig     `yaml:"tasks,omitempty"`
}

// MCPConfig configures the MCP transports and initialize-time
// metadata. Both transports are served from the same MCPServer (and
// therefore the same tool registrations); this struct just gates the
// legacy-SSE pair, lets operators override the instructions text, and
// exposes the SSE keepalive knobs that prevent intermediate proxies
// from closing long-lived streams during a slow pf_fault call.
type MCPConfig struct {
	// SSEEnabled toggles the legacy-SSE transport (GET /sse + POST
	// /message) alongside the streamable-http transport at /mcp.
	// Nil (unset) defaults to true so Claude Desktop and other
	// SSE-only clients work out of the box — set false to disable
	// if you only serve streamable-http clients.
	SSEEnabled *bool `yaml:"sse_enabled,omitempty"`
	// Instructions overrides the server-level instructions string
	// advertised in the MCP initialize response. Most clients
	// surface this in the agent's system prompt, so it is the
	// canonical place to teach an agent when to reach for pf_*
	// tools vs the built-ins. Empty means "use the built-in default
	// from internal/tool.DefaultInstructions".
	Instructions string `yaml:"instructions,omitempty"`
	// SSEKeepAlive toggles periodic JSON-RPC ping events on the
	// persistent GET /sse stream. Without keepalives, a long-running
	// tool call (e.g. a 60-120s pf_fault) leaves the SSE connection
	// idle the whole time and any intermediate proxy (nginx
	// proxy_read_timeout default 60s, Node undici headersTimeout
	// default 60s, …) may close the connection before the tool's
	// reply arrives. With keepalives enabled, pagefault emits a
	// `ping` event on a ticker so the connection stays active from
	// the proxy's perspective. Nil (unset) defaults to **true** —
	// the fix is purely additive (a few bytes every N seconds per
	// client) and the failure mode without it is hard to diagnose.
	// Explicit false opts out, e.g. for operators who want to
	// minimise traffic or who serve an SSE client that chokes on
	// unsolicited pings.
	SSEKeepAlive *bool `yaml:"sse_keepalive,omitempty"`
	// SSEKeepAliveIntervalSeconds is the ticker interval for the
	// keepalive pings described on SSEKeepAlive. Zero means "use
	// pagefault's safe default of 15 seconds", which is longer than
	// mcp-go's 10-second default but still comfortably under the
	// common 30 / 60 second proxy idle timeouts. Positive values
	// override the default; values below 1 are rounded up to 1.
	// Ignored when SSEKeepAlive is explicitly false.
	SSEKeepAliveIntervalSeconds int `yaml:"sse_keepalive_interval_seconds,omitempty"`
}

// SSEEnabledOrDefault returns whether the SSE transport should be
// mounted, defaulting to true when the field is unset.
func (m MCPConfig) SSEEnabledOrDefault() bool {
	if m.SSEEnabled == nil {
		return true
	}
	return *m.SSEEnabled
}

// SSEKeepAliveOrDefault returns whether the SSE transport should
// emit keepalive pings, defaulting to true when the field is unset.
// The failure mode without keepalives (intermediate proxies closing
// idle connections during long tool calls) is bad enough that we
// make the fix opt-out rather than opt-in.
func (m MCPConfig) SSEKeepAliveOrDefault() bool {
	if m.SSEKeepAlive == nil {
		return true
	}
	return *m.SSEKeepAlive
}

// SSEKeepAliveIntervalOrDefault returns the keepalive ticker
// interval in seconds, defaulting to 15 when the field is zero or
// unset. Values below 1 are clamped to 1.
func (m MCPConfig) SSEKeepAliveIntervalOrDefault() int {
	if m.SSEKeepAliveIntervalSeconds <= 0 {
		return 15
	}
	return m.SSEKeepAliveIntervalSeconds
}

// CORSConfig configures Cross-Origin Resource Sharing headers for the REST
// transport. Only origins in AllowedOrigins are echoed in the
// Access-Control-Allow-Origin header; the wildcard "*" is permitted when you
// explicitly want any origin. An empty AllowedOrigins list disables CORS
// (no headers emitted, preflight requests are not intercepted).
type CORSConfig struct {
	Enabled          bool     `yaml:"enabled,omitempty"`
	AllowedOrigins   []string `yaml:"allowed_origins,omitempty"`
	AllowedMethods   []string `yaml:"allowed_methods,omitempty"`
	AllowedHeaders   []string `yaml:"allowed_headers,omitempty"`
	AllowCredentials bool     `yaml:"allow_credentials,omitempty"`
	MaxAge           int      `yaml:"max_age,omitempty"` // seconds; default 600
}

// RateLimitConfig configures a per-caller token bucket applied to every
// authenticated request. RPS is the steady-state refill rate and Burst is
// the bucket size. Anonymous callers share a single bucket keyed on the
// literal caller id "anonymous".
type RateLimitConfig struct {
	Enabled bool    `yaml:"enabled,omitempty"`
	RPS     float64 `yaml:"rps,omitempty"`   // tokens per second; default 10
	Burst   int     `yaml:"burst,omitempty"` // bucket size; default 20
}

// TasksConfig configures the 0.10.0 async pf_fault task manager.
// Every pf_fault / pf_poke mode:agent call flows through the manager:
// the subagent spawn runs on a detached goroutine so the caller's
// HTTP request can disconnect without killing the subagent, and the
// caller polls pf_ps(task_id) for the result.
//
// The manager is in-memory only for 0.10.0 — task state is lost on
// restart, and a client that was mid-poll gets resource_not_found
// and has to re-issue the pf_fault. Persistence is deferred; see
// docs/architecture.md → "Task manager" for the rationale.
type TasksConfig struct {
	// TTLSeconds is how long a terminal task is kept in memory
	// before it is reclaimed on the next sweep. Zero means "use
	// the default of 600 (10 minutes)". Clients should poll
	// within this window; long-gone tasks are gone.
	TTLSeconds int `yaml:"ttl_seconds,omitempty"`
	// MaxConcurrent caps the number of in-flight task goroutines.
	// Zero means "use the default of 16". Submissions past the
	// cap return a rate-limited error so the HTTP response is a
	// 429 instead of a silent queue.
	MaxConcurrent int `yaml:"max_concurrent,omitempty"`
}

// TasksTTLOrDefault returns the task TTL in seconds, defaulting to
// 600 when unset.
func (t TasksConfig) TasksTTLOrDefault() int {
	if t.TTLSeconds <= 0 {
		return 600
	}
	return t.TTLSeconds
}

// TasksMaxConcurrentOrDefault returns the max_concurrent cap,
// defaulting to 16 when unset.
func (t TasksConfig) TasksMaxConcurrentOrDefault() int {
	if t.MaxConcurrent <= 0 {
		return 16
	}
	return t.MaxConcurrent
}

// AuthConfig configures the authentication layer.
//
// Mode selects the provider: "none", "bearer", "trusted_header", or "oauth2".
// The "oauth2" mode is a **compound** provider: OAuth2 client_credentials
// issued access tokens are accepted first, and — if `bearer.tokens_file` is
// also configured — long-lived bearer tokens from the JSONL store are
// accepted as a fallback. This lets operators migrate Claude Desktop to
// OAuth2 without breaking existing Claude Code deployments that rely on
// static bearer tokens.
type AuthConfig struct {
	Mode          string              `yaml:"mode" validate:"required,oneof=none bearer trusted_header oauth2"`
	Bearer        BearerAuthConfig    `yaml:"bearer,omitempty"`
	TrustedHeader TrustedHeaderConfig `yaml:"trusted_header,omitempty"`
	OAuth2        OAuth2Config        `yaml:"oauth2,omitempty"`
}

// BearerAuthConfig configures bearer-token authentication.
type BearerAuthConfig struct {
	// TokensFile is a JSONL file; each line is a token record:
	//   {"id": "...", "token": "pf_...", "label": "..."}
	TokensFile string `yaml:"tokens_file,omitempty"`
}

// TrustedHeaderConfig configures trusted-header authentication (behind a
// reverse proxy that has already authenticated the caller).
type TrustedHeaderConfig struct {
	Header         string   `yaml:"header,omitempty"`
	TrustedProxies []string `yaml:"trusted_proxies,omitempty"`
}

// OAuth2Config configures the OAuth2 provider (shipped in 0.7.0,
// extended in 0.8.0). The provider supports two grant types:
//
//   - client_credentials (0.7.0): machine-to-machine auth where the
//     client authenticates with a pre-registered client_secret.
//   - authorization_code + PKCE (0.8.0): the MCP-standard browser-
//     based flow that Claude Desktop requires. Public clients (no
//     secret) use PKCE to protect the code exchange.
//
// The provider mounts public endpoints at the server root:
//
//   - GET  /.well-known/oauth-protected-resource   (RFC 9728 pointer)
//   - GET  /.well-known/oauth-authorization-server (RFC 8414 metadata)
//   - POST /oauth/token                            (both grant types)
//   - GET  /oauth/authorize                        (authorization_code)
//   - POST /register                               (RFC 7591 DCR, when dcr_enabled)
//
// Clients are registered either out-of-band via `pagefault
// oauth-client create` or dynamically via RFC 7591 POST /register
// (when dcr_enabled is true). DCR creates public clients (no
// client_secret, PKCE-only) and is opt-in because it opens a
// zero-auth endpoint that creates client records.
type OAuth2Config struct {
	// ClientsFile is a JSONL file where each line is an OAuth2 client
	// record (id, label, bcrypt secret_hash, scopes, redirect_uris,
	// metadata). Required when Mode is "oauth2".
	ClientsFile string `yaml:"clients_file,omitempty"`
	// Issuer overrides the `iss` value advertised in the RFC 8414
	// metadata document and the `resource` value in RFC 9728. Empty
	// falls back to server.public_url, and if that is also empty the
	// handlers infer the issuer from the incoming request's scheme +
	// host — works for direct access, may misreport behind a reverse
	// proxy that rewrites hosts.
	Issuer string `yaml:"issuer,omitempty"`
	// AccessTokenTTLSeconds controls how long an issued access token
	// is valid. Zero means "use the default of 3600 (1 hour)". Claude
	// Desktop will re-exchange client_id/client_secret for a new
	// token automatically when its cached one expires, so a short
	// TTL is safe and limits the blast radius of a leaked token.
	AccessTokenTTLSeconds int `yaml:"access_token_ttl_seconds,omitempty"`
	// DefaultScopes is the scope list attached to every newly issued
	// token when the client does not request any. Empty means
	// ["mcp"] — a single broad scope that matches the MCP client
	// ecosystem convention. Fine-grained per-tool scopes are not
	// supported.
	DefaultScopes []string `yaml:"default_scopes,omitempty"`
	// AuthCodeTTLSeconds controls how long an authorization code is
	// valid. Zero means "use the default of 60 seconds". Short TTLs
	// limit the window for code interception; PKCE provides the
	// cryptographic protection against use by anyone other than the
	// code_verifier holder.
	AuthCodeTTLSeconds int `yaml:"auth_code_ttl_seconds,omitempty"`
	// AutoApprove controls whether GET /oauth/authorize immediately
	// redirects with an authorization code (true) or renders a
	// consent page for the operator to click (false). Nil (unset)
	// defaults to true — on a single-operator self-hosted server the
	// operator is authorizing themselves, so clicking a consent
	// button adds no security value.
	AutoApprove *bool `yaml:"auto_approve,omitempty"`
	// DCREnabled controls whether the RFC 7591 dynamic client
	// registration endpoint (POST /register) is mounted. Disabled by
	// default because DCR creates clients without authentication,
	// which is inappropriate for most single-operator deployments.
	// When enabled, MCP clients like Claude Desktop can self-register
	// as public OAuth2 clients without manual `oauth-client create`.
	DCREnabled *bool `yaml:"dcr_enabled,omitempty"`
	// DCRBearerToken is an optional bearer token that must be
	// presented in the Authorization header of POST /register
	// requests. When empty, registration is open (no auth required).
	// Set this to restrict DCR to clients that know the token.
	DCRBearerToken string `yaml:"dcr_bearer_token,omitempty"`
}

// AccessTokenTTLOrDefault returns the access token TTL as a Go
// time.Duration-compatible seconds value, defaulting to 3600.
func (o OAuth2Config) AccessTokenTTLOrDefault() int {
	if o.AccessTokenTTLSeconds <= 0 {
		return 3600
	}
	return o.AccessTokenTTLSeconds
}

// DefaultScopesOrDefault returns the default scope list, falling back
// to ["mcp"] when none are configured.
func (o OAuth2Config) DefaultScopesOrDefault() []string {
	if len(o.DefaultScopes) == 0 {
		return []string{"mcp"}
	}
	return o.DefaultScopes
}

// DCREnabledOrDefault returns whether DCR is enabled, defaulting to
// false when nil. DCR is opt-in because it opens a zero-auth endpoint
// that creates client records on the server.
func (o OAuth2Config) DCREnabledOrDefault() bool {
	if o.DCREnabled == nil {
		return false
	}
	return *o.DCREnabled
}

// AuthCodeTTLOrDefault returns the authorization code TTL in seconds,
// defaulting to 60 when zero or unset.
func (o OAuth2Config) AuthCodeTTLOrDefault() int {
	if o.AuthCodeTTLSeconds <= 0 {
		return 60
	}
	return o.AuthCodeTTLSeconds
}

// AutoApproveOrDefault returns whether the authorize endpoint should
// skip the consent page, defaulting to true when nil.
func (o OAuth2Config) AutoApproveOrDefault() bool {
	if o.AutoApprove == nil {
		return true
	}
	return *o.AutoApprove
}

// BackendConfig is a generic backend configuration. Type-specific fields are
// decoded into a type-specific struct by the backend registry.
//
// The Raw field preserves the full YAML node so backends can decode their own
// fields without every backend type polluting this struct.
type BackendConfig struct {
	Name string `yaml:"name" validate:"required"`
	Type string `yaml:"type" validate:"required"`

	// Raw is the full YAML node for this backend entry. Backends unmarshal
	// their type-specific fields from this node.
	Raw yaml.Node `yaml:"-"`
}

// UnmarshalYAML captures the full node so backends can decode their own fields.
func (b *BackendConfig) UnmarshalYAML(value *yaml.Node) error {
	// Decode the common fields.
	type rawBackend struct {
		Name string `yaml:"name"`
		Type string `yaml:"type"`
	}
	var rb rawBackend
	if err := value.Decode(&rb); err != nil {
		return err
	}
	b.Name = rb.Name
	b.Type = rb.Type
	b.Raw = *value
	return nil
}

// FilesystemBackendConfig is the configuration for a filesystem backend.
// It is decoded from BackendConfig.Raw by the filesystem backend constructor.
//
// Phase-4 write support: a backend is read-only unless Writable is set.
// When Writable is true, WritePaths limits which URIs accept writes,
// WriteMode selects between append-only and full-mutation semantics,
// MaxEntrySize caps a single write payload, and FileLocking picks the
// locking strategy (flock vs. none).
type FilesystemBackendConfig struct {
	Name      string              `yaml:"name" validate:"required"`
	Type      string              `yaml:"type" validate:"required,eq=filesystem"`
	Root      string              `yaml:"root" validate:"required"`
	Include   []string            `yaml:"include"`
	Exclude   []string            `yaml:"exclude"`
	URIScheme string              `yaml:"uri_scheme" validate:"required"`
	AutoTag   map[string][]string `yaml:"auto_tag,omitempty"`
	Sandbox   bool                `yaml:"sandbox"`

	// ── Write config (all optional; default is read-only) ──

	// Writable switches the backend from read-only to read-write. Every
	// other write field below is ignored unless this is true.
	Writable bool `yaml:"writable,omitempty"`
	// WritePaths is an allowlist of URI glob patterns that accept writes.
	// If empty and Writable is true, every URI that passes the read
	// include/exclude filter is also writable — which is rarely what you
	// want. Prefer specific entries like "memory://memory/20*.md".
	WritePaths []string `yaml:"write_paths,omitempty"`
	// WriteMode names the mutation policy. "append" (default) is the
	// safe mode; "any" is a second-tier operator opt-in whose only
	// current effect is unlocking pf_poke's format:"raw" (bytes
	// without the entry-template wrapper). Prepend and overwrite are
	// reserved for a future phase — setting "any" today does not
	// enable either operation, it just removes the raw-format gate.
	WriteMode string `yaml:"write_mode,omitempty" validate:"omitempty,oneof=append any"`
	// MaxEntrySize caps the size of a single write payload (in bytes)
	// before entry-template wrapping is applied. Zero means "unlimited"
	// but applyDefaults sets 2000 for writable backends so the default
	// is safe.
	MaxEntrySize int `yaml:"max_entry_size,omitempty"`
	// FileLocking selects the concurrency-safety strategy: "flock"
	// (POSIX advisory lock via syscall.Flock, default on writable
	// backends) or "none" (no locking, acceptable only when pagefault
	// is the sole writer). Non-writable backends ignore this field.
	FileLocking string `yaml:"file_locking,omitempty" validate:"omitempty,oneof=flock none"`
}

// SubprocessBackendConfig configures a generic subprocess-search backend
// (e.g., ripgrep). The backend runs Command with {query} substituted
// (shell-escaped) and {roots} substituted as space-joined Roots, parses
// stdout according to Parse, and returns SearchResults.
type SubprocessBackendConfig struct {
	Name    string   `yaml:"name" validate:"required"`
	Type    string   `yaml:"type" validate:"required,eq=subprocess"`
	Command string   `yaml:"command" validate:"required"`
	Roots   []string `yaml:"roots,omitempty"`
	Timeout int      `yaml:"timeout,omitempty"` // seconds; default 10
	// Parse is the stdout format: "ripgrep_json" (ripgrep --json),
	// "grep" (classic path:lineno:content), or "plain" (one snippet
	// per line). Default is "plain".
	Parse string `yaml:"parse,omitempty"`
}

// HTTPBackendAuth configures per-backend auth for an HTTP backend. Kept
// simple in Phase 2 — bearer tokens only. ${ENV} substitution is done by
// the root Config loader before this struct is decoded.
type HTTPBackendAuth struct {
	Mode  string `yaml:"mode,omitempty"`  // "none" | "bearer"
	Token string `yaml:"token,omitempty"` // Bearer token value
}

// HTTPBackendRequest is the request template used by an HTTPBackend for
// both search and (future) read calls.
type HTTPBackendRequest struct {
	Method       string            `yaml:"method,omitempty"` // default "POST"
	Path         string            `yaml:"path,omitempty"`
	Headers      map[string]string `yaml:"headers,omitempty"`
	BodyTemplate string            `yaml:"body_template,omitempty"`
	// ResponsePath is a dotted JSON path that extracts the result array
	// from the response body (e.g., "results" or "data.items"). If empty,
	// the whole body is expected to be a JSON array.
	ResponsePath string `yaml:"response_path,omitempty"`
}

// HTTPBackendConfig configures a generic HTTP search backend.
type HTTPBackendConfig struct {
	Name    string             `yaml:"name" validate:"required"`
	Type    string             `yaml:"type" validate:"required,eq=http"`
	BaseURL string             `yaml:"base_url" validate:"required"`
	Auth    HTTPBackendAuth    `yaml:"auth,omitempty"`
	Search  HTTPBackendRequest `yaml:"search,omitempty"`
	Timeout int                `yaml:"timeout,omitempty"` // seconds; default 15
}

// SubagentCLIBackendConfig configures a CLI subagent backend. The backend
// shells out using Command with {agent_id}, {task}, and {timeout}
// substituted at spawn time; the first argument is taken from the command
// template's first whitespace-delimited token.
//
// The two PromptTemplate fields frame raw caller content (query for
// pf_fault, content for pf_poke mode:agent) before it is substituted
// into Command. Leaving them empty uses the built-in defaults from
// internal/backend/prompt.go, which are prescriptive about memory
// retrieval / memory placement — the common failure mode is a
// subagent that drifts into generic Q&A because nothing framed the
// task. Per-agent overrides on AgentSpec take precedence.
type SubagentCLIBackendConfig struct {
	Name    string      `yaml:"name" validate:"required"`
	Type    string      `yaml:"type" validate:"required,eq=subagent-cli"`
	Command string      `yaml:"command" validate:"required"`
	Timeout int         `yaml:"timeout,omitempty"` // seconds; default 300
	Agents  []AgentSpec `yaml:"agents" validate:"required,min=1,dive"`

	// RetrievePromptTemplate wraps the task for pf_fault calls.
	// Placeholders: {task}, {time_range}, {agent_id}. Empty means
	// "use the built-in default".
	RetrievePromptTemplate string `yaml:"retrieve_prompt_template,omitempty"`
	// WritePromptTemplate wraps the task for pf_poke mode:agent
	// calls. Placeholders: {task}, {target}, {agent_id}. Empty
	// means "use the built-in default".
	WritePromptTemplate string `yaml:"write_prompt_template,omitempty"`
}

// SubagentHTTPBackendConfig configures an HTTP subagent backend. Spawn
// POSTs to {base_url}{spawn.path} with the configured body template.
//
// RetrievePromptTemplate / WritePromptTemplate work the same way as
// on the CLI backend — see SubagentCLIBackendConfig for semantics.
type SubagentHTTPBackendConfig struct {
	Name    string             `yaml:"name" validate:"required"`
	Type    string             `yaml:"type" validate:"required,eq=subagent-http"`
	BaseURL string             `yaml:"base_url" validate:"required"`
	Auth    HTTPBackendAuth    `yaml:"auth,omitempty"`
	Spawn   HTTPBackendRequest `yaml:"spawn" validate:"required"`
	Timeout int                `yaml:"timeout,omitempty"` // seconds; default 300
	Agents  []AgentSpec        `yaml:"agents" validate:"required,min=1,dive"`

	// RetrievePromptTemplate wraps the task for pf_fault calls.
	// Placeholders: {task}, {time_range}, {agent_id}. Empty means
	// "use the built-in default".
	RetrievePromptTemplate string `yaml:"retrieve_prompt_template,omitempty"`
	// WritePromptTemplate wraps the task for pf_poke mode:agent
	// calls. Placeholders: {task}, {target}, {agent_id}. Empty
	// means "use the built-in default".
	WritePromptTemplate string `yaml:"write_prompt_template,omitempty"`
}

// AgentSpec describes a single agent exposed by a subagent backend.
// Used for both CLI and HTTP variants. The two PromptTemplate fields
// are per-agent overrides — when set, they take precedence over the
// backend-level defaults in the containing Subagent*BackendConfig.
// This lets one backend host a mix of agent personas (a strict
// memory-retrieval agent alongside a freer summarisation agent, say)
// without needing a separate backend entry per persona.
type AgentSpec struct {
	ID          string `yaml:"id" validate:"required"`
	Description string `yaml:"description,omitempty"`

	// RetrievePromptTemplate overrides the backend's retrieve
	// template for this agent. Empty means "fall back to the
	// backend-level retrieve template, then to the built-in
	// default".
	RetrievePromptTemplate string `yaml:"retrieve_prompt_template,omitempty"`
	// WritePromptTemplate overrides the backend's write template
	// for this agent. Same fallback chain.
	WritePromptTemplate string `yaml:"write_prompt_template,omitempty"`
}

// ContextConfig defines a named bundle of backend sources.
type ContextConfig struct {
	Name        string          `yaml:"name" validate:"required"`
	Description string          `yaml:"description,omitempty"`
	Sources     []ContextSource `yaml:"sources" validate:"required,min=1,dive"`
	Format      string          `yaml:"format,omitempty"`   // "markdown" | "json"
	MaxSize     int             `yaml:"max_size,omitempty"` // characters
}

// ContextSource points to a resource on a named backend.
type ContextSource struct {
	Backend string         `yaml:"backend" validate:"required"`
	URI     string         `yaml:"uri" validate:"required"`
	Params  map[string]any `yaml:"params,omitempty"`
}

// ToolsConfig toggles individual tools on or off. Tool names follow the
// page-fault naming scheme — pf_maps, pf_load, pf_scan, pf_peek (Phase 1)
// and pf_fault, pf_ps, pf_poke (later phases).
//
// All *bool-valued fields default to enabled (true) when absent. Using *bool
// lets us distinguish "not set" from "set to false".
type ToolsConfig struct {
	PfMaps  *bool `yaml:"pf_maps,omitempty"`
	PfLoad  *bool `yaml:"pf_load,omitempty"`
	PfScan  *bool `yaml:"pf_scan,omitempty"`
	PfPeek  *bool `yaml:"pf_peek,omitempty"`
	PfFault *bool `yaml:"pf_fault,omitempty"`
	PfPs    *bool `yaml:"pf_ps,omitempty"`
	PfPoke  *bool `yaml:"pf_poke,omitempty"`
}

// Enabled returns whether the named tool is enabled. Unknown names default to
// disabled. Unset tools default to enabled.
func (t ToolsConfig) Enabled(name string) bool {
	pick := func(p *bool) bool {
		if p == nil {
			return true
		}
		return *p
	}
	switch name {
	case "pf_maps":
		return pick(t.PfMaps)
	case "pf_load":
		return pick(t.PfLoad)
	case "pf_scan":
		return pick(t.PfScan)
	case "pf_peek":
		return pick(t.PfPeek)
	case "pf_fault":
		return pick(t.PfFault)
	case "pf_ps":
		return pick(t.PfPs)
	case "pf_poke":
		return pick(t.PfPoke)
	default:
		return false
	}
}

// FiltersConfig holds the optional filter pipeline configuration.
type FiltersConfig struct {
	Enabled   bool             `yaml:"enabled"`
	Path      PathFilterConfig `yaml:"path,omitempty"`
	Tags      TagFilterConfig  `yaml:"tags,omitempty"`
	Redaction RedactionConfig  `yaml:"redaction,omitempty"`
}

// PathFilterConfig configures URI allow/deny globs.
//
// Allow/Deny govern every tool; WriteAllow/WriteDeny add a second,
// write-only layer that stacks on top of each backend's own
// `write_paths` allowlist. The intent is "read broadly, write
// narrowly" — a user can read from any memory:// URI but only
// append to a handful of specific files.
type PathFilterConfig struct {
	Allow      []string `yaml:"allow,omitempty"`
	Deny       []string `yaml:"deny,omitempty"`
	WriteAllow []string `yaml:"write_allow,omitempty"`
	WriteDeny  []string `yaml:"write_deny,omitempty"`
}

// TagFilterConfig configures tag allow/deny sets.
type TagFilterConfig struct {
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
}

// RedactionConfig configures the Phase-3 redaction filter.
type RedactionConfig struct {
	Enabled bool            `yaml:"enabled"`
	Rules   []RedactionRule `yaml:"rules,omitempty"`
}

// RedactionRule is a single regex → replacement rule.
type RedactionRule struct {
	Pattern     string `yaml:"pattern" validate:"required"`
	Replacement string `yaml:"replacement"`
}

// AuditConfig configures the audit logger.
type AuditConfig struct {
	Enabled        bool   `yaml:"enabled"`
	LogPath        string `yaml:"log_path,omitempty"`        // "jsonl" mode
	Mode           string `yaml:"mode,omitempty"`            // "jsonl" | "stdout" | "off"
	IncludeContent bool   `yaml:"include_content,omitempty"` // include full result in audit (warning: large)
}

// ErrValidation wraps a validation failure for callers that want to test with
// errors.Is.
var ErrValidation = errors.New("config validation failed")

// Load reads a YAML config file, expands ${ENV_VAR} references in its
// contents, unmarshals it into a *Config, applies defaults, and validates the
// result.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	return Parse(raw)
}

// Parse parses YAML config bytes (with ${ENV} substitution applied) into a
// validated *Config.
func Parse(raw []byte) (*Config, error) {
	expanded := os.ExpandEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate checks the config against validator tags and returns a wrapped
// ErrValidation on failure.
func (c *Config) Validate() error {
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(c); err != nil {
		return fmt.Errorf("%w: %s", ErrValidation, err.Error())
	}
	return nil
}

// applyDefaults fills in default values for optional fields.
func (c *Config) applyDefaults() {
	if c.Server.Host == "" {
		c.Server.Host = "127.0.0.1"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8444
	}
	if c.Server.CORS.Enabled {
		if len(c.Server.CORS.AllowedMethods) == 0 {
			c.Server.CORS.AllowedMethods = []string{"GET", "POST", "OPTIONS"}
		}
		if len(c.Server.CORS.AllowedHeaders) == 0 {
			c.Server.CORS.AllowedHeaders = []string{"Content-Type", "Authorization"}
		}
		if c.Server.CORS.MaxAge == 0 {
			c.Server.CORS.MaxAge = 600
		}
	}
	if c.Server.RateLimit.Enabled {
		if c.Server.RateLimit.RPS <= 0 {
			c.Server.RateLimit.RPS = 10
		}
		if c.Server.RateLimit.Burst <= 0 {
			c.Server.RateLimit.Burst = 20
		}
	}
	if c.Audit.Mode == "" {
		if c.Audit.LogPath != "" {
			c.Audit.Mode = "jsonl"
		} else if c.Audit.Enabled {
			c.Audit.Mode = "stdout"
		} else {
			c.Audit.Mode = "off"
		}
	}
	for i := range c.Contexts {
		if c.Contexts[i].Format == "" {
			c.Contexts[i].Format = "markdown"
		}
		if c.Contexts[i].MaxSize == 0 {
			c.Contexts[i].MaxSize = 16000
		}
	}
}

// DecodeFilesystemBackend extracts a FilesystemBackendConfig from a generic
// BackendConfig. Returns an error if the type is not "filesystem" or if
// decoding fails. Write-related defaults are applied here (see
// applyFilesystemWriteDefaults) so every downstream consumer sees the same
// effective config regardless of what was written in the YAML.
func DecodeFilesystemBackend(bc BackendConfig) (*FilesystemBackendConfig, error) {
	if bc.Type != "filesystem" {
		return nil, fmt.Errorf("config: backend %q: expected type filesystem, got %q", bc.Name, bc.Type)
	}
	var fs FilesystemBackendConfig
	if err := bc.Raw.Decode(&fs); err != nil {
		return nil, fmt.Errorf("config: backend %q: decode filesystem: %w", bc.Name, err)
	}
	fs.applyWriteDefaults()
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(&fs); err != nil {
		return nil, fmt.Errorf("config: backend %q: %w: %s", bc.Name, ErrValidation, err.Error())
	}
	return &fs, nil
}

// applyWriteDefaults fills in Phase-4 write fields. Non-writable backends
// leave every field at its zero value; writable backends pick up
// append-only semantics, a 2000-byte entry cap, and flock locking so a
// config that flips "writable: true" alone is still safe.
func (fs *FilesystemBackendConfig) applyWriteDefaults() {
	if !fs.Writable {
		return
	}
	if fs.WriteMode == "" {
		fs.WriteMode = "append"
	}
	if fs.MaxEntrySize <= 0 {
		fs.MaxEntrySize = 2000
	}
	if fs.FileLocking == "" {
		fs.FileLocking = "flock"
	}
}

// DecodeSubprocessBackend extracts a SubprocessBackendConfig from a generic
// BackendConfig.
func DecodeSubprocessBackend(bc BackendConfig) (*SubprocessBackendConfig, error) {
	if bc.Type != "subprocess" {
		return nil, fmt.Errorf("config: backend %q: expected type subprocess, got %q", bc.Name, bc.Type)
	}
	var sp SubprocessBackendConfig
	if err := bc.Raw.Decode(&sp); err != nil {
		return nil, fmt.Errorf("config: backend %q: decode subprocess: %w", bc.Name, err)
	}
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(&sp); err != nil {
		return nil, fmt.Errorf("config: backend %q: %w: %s", bc.Name, ErrValidation, err.Error())
	}
	return &sp, nil
}

// DecodeHTTPBackend extracts an HTTPBackendConfig from a generic
// BackendConfig.
func DecodeHTTPBackend(bc BackendConfig) (*HTTPBackendConfig, error) {
	if bc.Type != "http" {
		return nil, fmt.Errorf("config: backend %q: expected type http, got %q", bc.Name, bc.Type)
	}
	var h HTTPBackendConfig
	if err := bc.Raw.Decode(&h); err != nil {
		return nil, fmt.Errorf("config: backend %q: decode http: %w", bc.Name, err)
	}
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(&h); err != nil {
		return nil, fmt.Errorf("config: backend %q: %w: %s", bc.Name, ErrValidation, err.Error())
	}
	return &h, nil
}

// DecodeSubagentCLIBackend extracts a SubagentCLIBackendConfig from a
// generic BackendConfig.
func DecodeSubagentCLIBackend(bc BackendConfig) (*SubagentCLIBackendConfig, error) {
	if bc.Type != "subagent-cli" {
		return nil, fmt.Errorf("config: backend %q: expected type subagent-cli, got %q", bc.Name, bc.Type)
	}
	var s SubagentCLIBackendConfig
	if err := bc.Raw.Decode(&s); err != nil {
		return nil, fmt.Errorf("config: backend %q: decode subagent-cli: %w", bc.Name, err)
	}
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(&s); err != nil {
		return nil, fmt.Errorf("config: backend %q: %w: %s", bc.Name, ErrValidation, err.Error())
	}
	return &s, nil
}

// DecodeSubagentHTTPBackend extracts a SubagentHTTPBackendConfig from a
// generic BackendConfig.
func DecodeSubagentHTTPBackend(bc BackendConfig) (*SubagentHTTPBackendConfig, error) {
	if bc.Type != "subagent-http" {
		return nil, fmt.Errorf("config: backend %q: expected type subagent-http, got %q", bc.Name, bc.Type)
	}
	var s SubagentHTTPBackendConfig
	if err := bc.Raw.Decode(&s); err != nil {
		return nil, fmt.Errorf("config: backend %q: decode subagent-http: %w", bc.Name, err)
	}
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(&s); err != nil {
		return nil, fmt.Errorf("config: backend %q: %w: %s", bc.Name, ErrValidation, err.Error())
	}
	return &s, nil
}
