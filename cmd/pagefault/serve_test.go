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
	// Phase 1 only supports filesystem; anything else should surface a
	// clear error from buildDispatcher.
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 8444},
		Auth:   config.AuthConfig{Mode: "none"},
		Backends: []config.BackendConfig{
			{Name: "remote", Type: "http"},
		},
	}
	_, _, err := buildDispatcher(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type")
	assert.Contains(t, err.Error(), "http")
}
