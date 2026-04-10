package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const minimalYAML = `
server:
  host: "127.0.0.1"
  port: 8444
auth:
  mode: "none"
backends:
  - name: fs
    type: filesystem
    root: "./demo-data"
    include: ["**/*.md"]
    exclude: []
    uri_scheme: "memory"
    sandbox: true
contexts:
  - name: demo
    description: "Demo context"
    sources:
      - backend: fs
        uri: "memory://README.md"
    format: markdown
    max_size: 4000
filters:
  enabled: false
audit:
  enabled: true
  log_path: "/tmp/pagefault-audit.jsonl"
`

func TestParse_MinimalValid(t *testing.T) {
	cfg, err := Parse([]byte(minimalYAML))
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "127.0.0.1", cfg.Server.Host)
	assert.Equal(t, 8444, cfg.Server.Port)
	assert.Equal(t, "none", cfg.Auth.Mode)

	require.Len(t, cfg.Backends, 1)
	assert.Equal(t, "fs", cfg.Backends[0].Name)
	assert.Equal(t, "filesystem", cfg.Backends[0].Type)

	require.Len(t, cfg.Contexts, 1)
	assert.Equal(t, "demo", cfg.Contexts[0].Name)
	assert.Equal(t, "markdown", cfg.Contexts[0].Format)
	assert.Equal(t, 4000, cfg.Contexts[0].MaxSize)

	assert.False(t, cfg.Filters.Enabled)
	assert.True(t, cfg.Audit.Enabled)
	assert.Equal(t, "jsonl", cfg.Audit.Mode, "mode should default to jsonl when log_path is set")
}

func TestParse_EnvSubstitution(t *testing.T) {
	t.Setenv("PF_TEST_TOKEN", "supersecret-token-value")

	yamlWithEnv := `
server:
  host: "127.0.0.1"
  port: 8444
auth:
  mode: "bearer"
  bearer:
    tokens_file: "${PF_TEST_TOKEN}"
backends:
  - name: fs
    type: filesystem
    root: "/tmp"
    include: ["**/*.md"]
    uri_scheme: "memory"
    sandbox: true
`
	cfg, err := Parse([]byte(yamlWithEnv))
	require.NoError(t, err)
	assert.Equal(t, "supersecret-token-value", cfg.Auth.Bearer.TokensFile)
}

func TestParse_Defaults(t *testing.T) {
	// Omit host, port; verify defaults are applied.
	minimal := `
server:
  host: "0.0.0.0"
  port: 9000
auth:
  mode: "none"
backends:
  - name: fs
    type: filesystem
    root: "/tmp"
    include: ["**/*"]
    uri_scheme: "memory"
    sandbox: true
contexts:
  - name: demo
    sources:
      - backend: fs
        uri: "memory://x.md"
`
	cfg, err := Parse([]byte(minimal))
	require.NoError(t, err)
	assert.Equal(t, "markdown", cfg.Contexts[0].Format, "context format default")
	assert.Equal(t, 16000, cfg.Contexts[0].MaxSize, "context max_size default")
	assert.Equal(t, "off", cfg.Audit.Mode, "audit mode default when disabled")
}

func TestParse_CORSDefaults(t *testing.T) {
	// Enabled with no other fields set — defaults should fill methods/headers/max_age.
	raw := `
server:
  host: "127.0.0.1"
  port: 8444
  cors:
    enabled: true
    allowed_origins: ["https://example.com"]
auth:
  mode: "none"
backends:
  - name: fs
    type: filesystem
    root: "/tmp"
    include: ["**/*"]
    uri_scheme: "memory"
`
	cfg, err := Parse([]byte(raw))
	require.NoError(t, err)
	assert.True(t, cfg.Server.CORS.Enabled)
	assert.Equal(t, []string{"https://example.com"}, cfg.Server.CORS.AllowedOrigins)
	assert.ElementsMatch(t, []string{"GET", "POST", "OPTIONS"}, cfg.Server.CORS.AllowedMethods)
	assert.ElementsMatch(t, []string{"Content-Type", "Authorization"}, cfg.Server.CORS.AllowedHeaders)
	assert.Equal(t, 600, cfg.Server.CORS.MaxAge)
}

func TestParse_CORSDisabledByDefault(t *testing.T) {
	raw := `
server:
  host: "127.0.0.1"
  port: 8444
auth:
  mode: "none"
backends:
  - name: fs
    type: filesystem
    root: "/tmp"
    include: ["**/*"]
    uri_scheme: "memory"
`
	cfg, err := Parse([]byte(raw))
	require.NoError(t, err)
	assert.False(t, cfg.Server.CORS.Enabled)
	// Defaults only apply when enabled; disabled config stays empty.
	assert.Empty(t, cfg.Server.CORS.AllowedMethods)
}

func TestParse_RateLimitDefaults(t *testing.T) {
	raw := `
server:
  host: "127.0.0.1"
  port: 8444
  rate_limit:
    enabled: true
auth:
  mode: "none"
backends:
  - name: fs
    type: filesystem
    root: "/tmp"
    include: ["**/*"]
    uri_scheme: "memory"
`
	cfg, err := Parse([]byte(raw))
	require.NoError(t, err)
	assert.True(t, cfg.Server.RateLimit.Enabled)
	assert.Equal(t, 10.0, cfg.Server.RateLimit.RPS)
	assert.Equal(t, 20, cfg.Server.RateLimit.Burst)
}

func TestParse_RateLimitExplicit(t *testing.T) {
	raw := `
server:
  host: "127.0.0.1"
  port: 8444
  rate_limit:
    enabled: true
    rps: 5
    burst: 8
auth:
  mode: "none"
backends:
  - name: fs
    type: filesystem
    root: "/tmp"
    include: ["**/*"]
    uri_scheme: "memory"
`
	cfg, err := Parse([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, 5.0, cfg.Server.RateLimit.RPS)
	assert.Equal(t, 8, cfg.Server.RateLimit.Burst)
}

func TestParse_InvalidYAML(t *testing.T) {
	_, err := Parse([]byte("this is : not : valid : yaml : [["))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestParse_MissingRequired(t *testing.T) {
	// Missing backends — should fail validation.
	bad := `
server:
  host: "127.0.0.1"
  port: 8444
auth:
  mode: "none"
`
	_, err := Parse([]byte(bad))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrValidation), "expected ErrValidation, got %v", err)
}

func TestParse_InvalidAuthMode(t *testing.T) {
	bad := `
server:
  host: "127.0.0.1"
  port: 8444
auth:
  mode: "magic"
backends:
  - name: fs
    type: filesystem
    root: "/tmp"
    include: ["**/*"]
    uri_scheme: "memory"
    sandbox: true
`
	_, err := Parse([]byte(bad))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrValidation))
}

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(minimalYAML), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, 8444, cfg.Server.Port)
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	require.Error(t, err)
}

func TestToolsConfig_Enabled_DefaultEnabled(t *testing.T) {
	var t0 ToolsConfig // all nil
	assert.True(t, t0.Enabled("pf_maps"))
	assert.True(t, t0.Enabled("pf_load"))
	assert.True(t, t0.Enabled("pf_scan"))
	assert.True(t, t0.Enabled("pf_peek"))
	assert.True(t, t0.Enabled("pf_poke"))
	assert.False(t, t0.Enabled("nonexistent"))
}

func TestToolsConfig_Enabled_ExplicitlyDisabled(t *testing.T) {
	f := false
	tc := ToolsConfig{PfScan: &f, PfPoke: &f}
	assert.False(t, tc.Enabled("pf_scan"))
	assert.False(t, tc.Enabled("pf_poke"))
	assert.True(t, tc.Enabled("pf_peek"), "unset tools should default to enabled")
}

func TestDecodeFilesystemBackend(t *testing.T) {
	cfg, err := Parse([]byte(minimalYAML))
	require.NoError(t, err)

	fs, err := DecodeFilesystemBackend(cfg.Backends[0])
	require.NoError(t, err)
	assert.Equal(t, "fs", fs.Name)
	assert.Equal(t, "./demo-data", fs.Root)
	assert.Equal(t, "memory", fs.URIScheme)
	assert.True(t, fs.Sandbox)
	assert.Contains(t, fs.Include, "**/*.md")
}

func TestDecodeFilesystemBackend_WrongType(t *testing.T) {
	bc := BackendConfig{Name: "foo", Type: "subprocess"}
	_, err := DecodeFilesystemBackend(bc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected type filesystem")
}

// parseSingleBackend is a test helper that parses a minimal config
// containing a single non-filesystem backend defined by the caller's
// YAML snippet. The helper adds the server/auth/filesystem scaffolding
// needed to satisfy the top-level validator and returns the extra
// backend at index 1 (index 0 is the scaffold `fs` backend).
func parseSingleBackend(t *testing.T, snippet string) BackendConfig {
	t.Helper()
	yaml := `
server:
  host: "127.0.0.1"
  port: 8444
auth:
  mode: "none"
backends:
  - name: fs
    type: filesystem
    root: "/tmp"
    include: ["**/*.md"]
    uri_scheme: "memory"
    sandbox: true
` + snippet
	cfg, err := Parse([]byte(yaml))
	require.NoError(t, err)
	require.Len(t, cfg.Backends, 2, "snippet must contribute exactly one extra backend")
	return cfg.Backends[1]
}

// ───────────── DecodeSubprocessBackend ─────────────

func TestDecodeSubprocessBackend_HappyPath(t *testing.T) {
	bc := parseSingleBackend(t, `
  - name: rg
    type: subprocess
    command: "rg --json -n {query} {roots}"
    roots:
      - "/home/jet/notes"
    timeout: 7
    parse: "ripgrep_json"
`)
	got, err := DecodeSubprocessBackend(bc)
	require.NoError(t, err)
	assert.Equal(t, "rg", got.Name)
	assert.Equal(t, "rg --json -n {query} {roots}", got.Command)
	assert.Equal(t, []string{"/home/jet/notes"}, got.Roots)
	assert.Equal(t, 7, got.Timeout)
	assert.Equal(t, "ripgrep_json", got.Parse)
}

func TestDecodeSubprocessBackend_WrongType(t *testing.T) {
	bc := BackendConfig{Name: "x", Type: "filesystem"}
	_, err := DecodeSubprocessBackend(bc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected type subprocess")
}

func TestDecodeSubprocessBackend_MissingCommand(t *testing.T) {
	bc := parseSingleBackend(t, `
  - name: rg
    type: subprocess
    timeout: 5
`)
	_, err := DecodeSubprocessBackend(bc)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "Command")
}

// ───────────── DecodeHTTPBackend ─────────────

func TestDecodeHTTPBackend_HappyPath(t *testing.T) {
	bc := parseSingleBackend(t, `
  - name: lcm
    type: http
    base_url: "http://127.0.0.1:6443"
    auth:
      mode: "bearer"
      token: "abc"
    search:
      method: "POST"
      path: "/api/lcm/search"
      body_template: '{"q":"{query}"}'
      response_path: "results"
    timeout: 12
`)
	got, err := DecodeHTTPBackend(bc)
	require.NoError(t, err)
	assert.Equal(t, "lcm", got.Name)
	assert.Equal(t, "http://127.0.0.1:6443", got.BaseURL)
	assert.Equal(t, "bearer", got.Auth.Mode)
	assert.Equal(t, "abc", got.Auth.Token)
	assert.Equal(t, "POST", got.Search.Method)
	assert.Equal(t, "/api/lcm/search", got.Search.Path)
	assert.Equal(t, "results", got.Search.ResponsePath)
	assert.Equal(t, 12, got.Timeout)
}

func TestDecodeHTTPBackend_WrongType(t *testing.T) {
	bc := BackendConfig{Name: "x", Type: "filesystem"}
	_, err := DecodeHTTPBackend(bc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected type http")
}

func TestDecodeHTTPBackend_MissingBaseURL(t *testing.T) {
	bc := parseSingleBackend(t, `
  - name: lcm
    type: http
    search:
      path: "/s"
`)
	_, err := DecodeHTTPBackend(bc)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "BaseURL")
}

// ───────────── DecodeSubagentCLIBackend ─────────────

func TestDecodeSubagentCLIBackend_HappyPath(t *testing.T) {
	bc := parseSingleBackend(t, `
  - name: openclaw
    type: subagent-cli
    command: "openclaw run --agent {agent_id} --task {task}"
    timeout: 300
    agents:
      - id: wocha
        description: "dev agent"
      - id: main
        description: "primary"
`)
	got, err := DecodeSubagentCLIBackend(bc)
	require.NoError(t, err)
	assert.Equal(t, "openclaw", got.Name)
	assert.Equal(t, 300, got.Timeout)
	require.Len(t, got.Agents, 2)
	assert.Equal(t, "wocha", got.Agents[0].ID)
	assert.Equal(t, "dev agent", got.Agents[0].Description)
	assert.Equal(t, "main", got.Agents[1].ID)
}

func TestDecodeSubagentCLIBackend_WrongType(t *testing.T) {
	bc := BackendConfig{Name: "x", Type: "subagent-http"}
	_, err := DecodeSubagentCLIBackend(bc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected type subagent-cli")
}

func TestDecodeSubagentCLIBackend_MissingAgents(t *testing.T) {
	bc := parseSingleBackend(t, `
  - name: openclaw
    type: subagent-cli
    command: "echo hi"
`)
	_, err := DecodeSubagentCLIBackend(bc)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "Agents")
}

func TestDecodeSubagentCLIBackend_MissingCommand(t *testing.T) {
	bc := parseSingleBackend(t, `
  - name: openclaw
    type: subagent-cli
    agents:
      - id: alpha
`)
	_, err := DecodeSubagentCLIBackend(bc)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "Command")
}

// ───────────── DecodeSubagentHTTPBackend ─────────────

func TestDecodeSubagentHTTPBackend_HappyPath(t *testing.T) {
	bc := parseSingleBackend(t, `
  - name: oc-http
    type: subagent-http
    base_url: "https://localhost:6443/api"
    auth:
      mode: "bearer"
      token: "tok"
    spawn:
      method: "POST"
      path: "/agents/{agent_id}/run"
      body_template: '{"task":"{task}"}'
      response_path: "result"
    timeout: 180
    agents:
      - id: wocha
        description: "dev agent"
`)
	got, err := DecodeSubagentHTTPBackend(bc)
	require.NoError(t, err)
	assert.Equal(t, "oc-http", got.Name)
	assert.Equal(t, "https://localhost:6443/api", got.BaseURL)
	assert.Equal(t, "bearer", got.Auth.Mode)
	assert.Equal(t, "tok", got.Auth.Token)
	assert.Equal(t, "POST", got.Spawn.Method)
	assert.Equal(t, "/agents/{agent_id}/run", got.Spawn.Path)
	assert.Equal(t, "result", got.Spawn.ResponsePath)
	assert.Equal(t, 180, got.Timeout)
	require.Len(t, got.Agents, 1)
	assert.Equal(t, "wocha", got.Agents[0].ID)
}

func TestDecodeSubagentHTTPBackend_WrongType(t *testing.T) {
	bc := BackendConfig{Name: "x", Type: "subagent-cli"}
	_, err := DecodeSubagentHTTPBackend(bc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected type subagent-http")
}

func TestDecodeSubagentHTTPBackend_MissingBaseURL(t *testing.T) {
	bc := parseSingleBackend(t, `
  - name: oc
    type: subagent-http
    spawn:
      path: "/run"
    agents:
      - id: alpha
`)
	_, err := DecodeSubagentHTTPBackend(bc)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "BaseURL")
}

func TestDecodeSubagentHTTPBackend_MissingAgents(t *testing.T) {
	bc := parseSingleBackend(t, `
  - name: oc
    type: subagent-http
    base_url: "http://x"
    spawn:
      path: "/run"
`)
	_, err := DecodeSubagentHTTPBackend(bc)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "Agents")
}
