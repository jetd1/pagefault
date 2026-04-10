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
	assert.True(t, t0.Enabled("list_contexts"))
	assert.True(t, t0.Enabled("get_context"))
	assert.True(t, t0.Enabled("search"))
	assert.True(t, t0.Enabled("read"))
	assert.True(t, t0.Enabled("write"))
	assert.False(t, t0.Enabled("nonexistent"))
}

func TestToolsConfig_Enabled_ExplicitlyDisabled(t *testing.T) {
	f := false
	tc := ToolsConfig{Search: &f, Write: &f}
	assert.False(t, tc.Enabled("search"))
	assert.False(t, tc.Enabled("write"))
	assert.True(t, tc.Enabled("read"), "unset tools should default to enabled")
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
