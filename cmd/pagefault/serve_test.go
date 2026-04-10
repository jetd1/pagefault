package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/config"
)

// writeTestConfig writes a minimal pagefault.yaml and the referenced data
// directory into dir, returning the yaml path. The config has a single
// filesystem backend, one context, and all tools enabled.
func writeTestConfig(t *testing.T, dir string) string {
	t.Helper()
	dataDir := filepath.Join(dir, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "README.md"), []byte("# hello\n\nworld\n"), 0o600))

	yaml := "" +
		"server:\n" +
		"  host: \"127.0.0.1\"\n" +
		"  port: 8444\n" +
		"auth:\n" +
		"  mode: \"none\"\n" +
		"backends:\n" +
		"  - name: fs\n" +
		"    type: filesystem\n" +
		"    root: \"" + dataDir + "\"\n" +
		"    include: [\"**/*.md\"]\n" +
		"    uri_scheme: \"memory\"\n" +
		"    sandbox: true\n" +
		"contexts:\n" +
		"  - name: demo\n" +
		"    sources:\n" +
		"      - backend: fs\n" +
		"        uri: \"memory://README.md\"\n" +
		"    max_size: 4000\n" +
		"tools:\n" +
		"  pf_maps: true\n" +
		"  pf_load: true\n" +
		"  pf_scan: true\n" +
		"  pf_peek: true\n" +
		"filters:\n" +
		"  enabled: false\n" +
		"audit:\n" +
		"  enabled: false\n"

	cfgPath := filepath.Join(dir, "pagefault.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))
	return cfgPath
}

func TestBuildDispatcher_Minimal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	d, closer, err := buildDispatcher(cfg)
	require.NoError(t, err)
	require.NotNil(t, d)
	require.NotNil(t, closer)
	defer func() { _ = closer() }()

	// Every declared backend should be registered and reachable.
	assert.Equal(t, []string{"fs"}, d.SortedBackendNames())

	// Every enabled tool should report enabled.
	for _, name := range []string{"pf_maps", "pf_load", "pf_scan", "pf_peek"} {
		assert.True(t, d.ToolEnabled(name), "tool %q should be enabled", name)
	}
}

func TestBuildDispatcher_UnsupportedBackend(t *testing.T) {
	// Any backend type the dispatcher builder doesn't know about must
	// surface a clear error.
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 8444},
		Auth:   config.AuthConfig{Mode: "none"},
		Backends: []config.BackendConfig{
			{Name: "remote", Type: "telepath"},
		},
	}
	_, _, err := buildDispatcher(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type")
	assert.Contains(t, err.Error(), "telepath")
}

// TestBuildDispatcher_Phase2Backends loads a config that exercises every
// Phase-2 backend type (subprocess, http, subagent-cli, subagent-http)
// and verifies buildDispatcher wires them all without error. Uses only
// the constructors — no network/process calls actually happen.
func TestBuildDispatcher_Phase2Backends(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "x.md"), []byte("x\n"), 0o600))

	yaml := "" +
		"server:\n" +
		"  host: \"127.0.0.1\"\n" +
		"  port: 8444\n" +
		"auth:\n" +
		"  mode: \"none\"\n" +
		"backends:\n" +
		"  - name: fs\n" +
		"    type: filesystem\n" +
		"    root: \"" + dataDir + "\"\n" +
		"    include: [\"**/*.md\"]\n" +
		"    uri_scheme: \"memory\"\n" +
		"    sandbox: true\n" +
		"  - name: rg\n" +
		"    type: subprocess\n" +
		"    command: \"echo {query}\"\n" +
		"    parse: \"plain\"\n" +
		"    timeout: 5\n" +
		"  - name: lcm\n" +
		"    type: http\n" +
		"    base_url: \"http://127.0.0.1:65535\"\n" +
		"    search:\n" +
		"      path: \"/search\"\n" +
		"      body_template: '{\"q\":\"{query}\"}'\n" +
		"      response_path: \"results\"\n" +
		"  - name: sa_cli\n" +
		"    type: subagent-cli\n" +
		"    command: \"echo hi\"\n" +
		"    timeout: 10\n" +
		"    agents:\n" +
		"      - id: alpha\n" +
		"        description: \"cli agent\"\n" +
		"  - name: sa_http\n" +
		"    type: subagent-http\n" +
		"    base_url: \"http://127.0.0.1:65535\"\n" +
		"    spawn:\n" +
		"      path: \"/agents/{agent_id}/run\"\n" +
		"      body_template: '{\"task\":\"{task}\"}'\n" +
		"      response_path: \"result\"\n" +
		"    agents:\n" +
		"      - id: beta\n" +
		"        description: \"http agent\"\n" +
		"contexts: []\n" +
		"tools:\n" +
		"  pf_maps: true\n" +
		"  pf_load: false\n" +
		"  pf_fault: true\n" +
		"  pf_ps: true\n" +
		"filters:\n" +
		"  enabled: false\n" +
		"audit:\n" +
		"  enabled: false\n"

	cfgPath := filepath.Join(dir, "pagefault.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	d, closer, err := buildDispatcher(cfg)
	require.NoError(t, err)
	require.NotNil(t, d)
	defer func() { _ = closer() }()

	assert.ElementsMatch(t,
		[]string{"fs", "rg", "lcm", "sa_cli", "sa_http"},
		d.SortedBackendNames())
	assert.True(t, d.ToolEnabled("pf_fault"))
	assert.True(t, d.ToolEnabled("pf_ps"))
}
