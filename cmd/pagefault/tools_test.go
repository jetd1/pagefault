package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/tool"
)

// ─────────────────── resolveConfigPath ───────────────────

func TestResolveConfigPath_ExplicitFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pagefault.yaml")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	got, err := resolveConfigPath(path)
	require.NoError(t, err)
	assert.Equal(t, path, got)
}

func TestResolveConfigPath_EnvFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "from-env.yaml")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	t.Setenv("PAGEFAULT_CONFIG", path)
	got, err := resolveConfigPath("")
	require.NoError(t, err)
	assert.Equal(t, path, got)
}

func TestResolveConfigPath_CwdFallback(t *testing.T) {
	// Chdir into a tempdir that contains ./pagefault.yaml. Clear the
	// env var so it doesn't short-circuit.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pagefault.yaml"), []byte("x"), 0o600))
	t.Setenv("PAGEFAULT_CONFIG", "")
	t.Chdir(dir)

	got, err := resolveConfigPath("")
	require.NoError(t, err)
	// resolveConfigPath returns the literal "./pagefault.yaml" (cwd
	// fallback), not an absolute path.
	assert.Equal(t, "./pagefault.yaml", got)
}

func TestResolveConfigPath_NotFound(t *testing.T) {
	t.Setenv("PAGEFAULT_CONFIG", "")
	t.Chdir(t.TempDir()) // empty dir, no pagefault.yaml
	_, err := resolveConfigPath("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config not found")
}

// ─────────────────── maps ───────────────────

func TestRunMaps_Text(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runMaps([]string{"--config", cfgPath}))
	})
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "DESCRIPTION")
	assert.Contains(t, out, "demo")
}

func TestRunMaps_JSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runMaps([]string{"--config", cfgPath, "--json"}))
	})
	var decoded tool.ListContextsOutput
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.Len(t, decoded.Contexts, 1)
	assert.Equal(t, "demo", decoded.Contexts[0].Name)
}

func TestRunMaps_ConfigNotFound(t *testing.T) {
	t.Setenv("PAGEFAULT_CONFIG", "")
	t.Chdir(t.TempDir())
	err := runMaps(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config not found")
}

// ─────────────────── load ───────────────────

func TestRunLoad_Text(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runLoad([]string{"--config", cfgPath, "demo"}))
	})
	// writeTestConfig seeds data/README.md with "# hello\n\nworld\n".
	assert.Contains(t, out, "hello")
	assert.Contains(t, out, "world")
	// pf_load prefixes each source with a "# <uri>" header.
	assert.Contains(t, out, "memory://README.md")
}

func TestRunLoad_JSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runLoad([]string{"--config", cfgPath, "--json", "demo"}))
	})
	var decoded tool.GetContextOutput
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	assert.Equal(t, "demo", decoded.Name)
	assert.Contains(t, decoded.Content, "hello")
}

func TestRunLoad_MissingName(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	err := runLoad([]string{"--config", cfgPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

func TestRunLoad_UnknownName(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	err := runLoad([]string{"--config", cfgPath, "does-not-exist"})
	require.Error(t, err)
}

// ─────────────────── scan ───────────────────

func TestRunScan_Text(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runScan([]string{"--config", cfgPath, "hello"}))
	})
	assert.Contains(t, out, "BACKEND")
	assert.Contains(t, out, "URI")
	assert.Contains(t, out, "fs")
	assert.Contains(t, out, "memory://README.md")
}

func TestRunScan_JSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runScan([]string{"--config", cfgPath, "--json", "hello"}))
	})
	var decoded tool.SearchOutput
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.NotEmpty(t, decoded.Results)
	assert.Equal(t, "fs", decoded.Results[0].Backend)
}

func TestRunScan_MultiWordQuery(t *testing.T) {
	// Positional args should be joined with spaces so the user can type
	// `pagefault scan <multi word phrase>` without quoting. writeTestConfig
	// seeds README.md with "# hello\n\nworld\n"; "# hello" is a two-token
	// phrase that appears literally on line 1.
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	out := captureStdout(t, func() {
		require.NoError(t, runScan([]string{"--config", cfgPath, "#", "hello"}))
	})
	assert.Contains(t, out, "memory://README.md")
}

func TestRunScan_NoMatches(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	out := captureStdout(t, func() {
		require.NoError(t, runScan([]string{"--config", cfgPath, "zzzzz-no-such-term-zzzzz"}))
	})
	assert.Contains(t, out, "no matches")
}

func TestRunScan_BackendRestriction(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	// Restricting to an unknown backend should surface an error.
	err := runScan([]string{"--config", cfgPath, "--backends", "nope", "hello"})
	require.Error(t, err)
}

func TestRunScan_MissingQuery(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	err := runScan([]string{"--config", cfgPath})
	require.Error(t, err)
}

// ─────────────────── peek ───────────────────

func TestRunPeek_Text(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runPeek([]string{"--config", cfgPath, "memory://README.md"}))
	})
	assert.Contains(t, out, "hello")
	assert.Contains(t, out, "world")
}

func TestRunPeek_LineRange(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runPeek([]string{"--config", cfgPath, "--from", "1", "--to", "1", "memory://README.md"}))
	})
	// Line 1 of the seeded README is "# hello"; line 3 is "world".
	assert.Contains(t, out, "# hello")
	assert.NotContains(t, out, "world")
}

func TestRunPeek_JSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runPeek([]string{"--config", cfgPath, "--json", "memory://README.md"}))
	})
	var decoded tool.ReadOutput
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.NotNil(t, decoded.Resource)
	assert.Equal(t, "memory://README.md", decoded.Resource.URI)
}

func TestRunPeek_MissingURI(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	err := runPeek([]string{"--config", cfgPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

func TestRunPeek_UnknownResource(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	err := runPeek([]string{"--config", cfgPath, "memory://nope.md"})
	require.Error(t, err)
}

// ─────────────────── env + cwd fallback end-to-end ───────────────────

func TestRunMaps_UsesEnvConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	t.Setenv("PAGEFAULT_CONFIG", cfgPath)

	out := captureStdout(t, func() {
		require.NoError(t, runMaps(nil))
	})
	assert.Contains(t, out, "demo")
}

// ─────────────────── --no-filter ───────────────────

func TestRunPeek_NoFilterBypassesFilters(t *testing.T) {
	// Write a config that denies the seeded file via a path filter.
	// Without --no-filter the peek should fail; with --no-filter it
	// should succeed.
	dir := t.TempDir()
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
		"filters:\n" +
		"  enabled: true\n" +
		"  path:\n" +
		"    deny: [\"memory://README.md\"]\n" +
		"audit:\n" +
		"  enabled: false\n"
	cfgPath := filepath.Join(dir, "pagefault.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))

	// Without --no-filter: blocked.
	err := runPeek([]string{"--config", cfgPath, "memory://README.md"})
	require.Error(t, err, "peek should be denied by path filter")

	// With --no-filter: allowed.
	out := captureStdout(t, func() {
		require.NoError(t, runPeek([]string{"--config", cfgPath, "--no-filter", "memory://README.md"}))
	})
	assert.Contains(t, out, "hello")
}

// ─────────────────── parseInterspersed / positional ordering ───────────────────

func TestRunPeek_PositionalBeforeFlags(t *testing.T) {
	// Regression: Go's stdlib flag.Parse stops at the first non-flag
	// token. parseInterspersed hoists flags past positionals, so
	// `peek <uri> --config X` works the same as `peek --config X <uri>`.
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runPeek([]string{"memory://README.md", "--config", cfgPath, "--from", "1", "--to", "1"}))
	})
	assert.Contains(t, out, "# hello")
	assert.NotContains(t, out, "world")
}

func TestRunScan_FlagsAfterQuery(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runScan([]string{"hello", "--config", cfgPath, "--limit", "5"}))
	})
	assert.Contains(t, out, "memory://README.md")
}

func TestRunPeek_DoubleDashTerminator(t *testing.T) {
	// Everything after `--` should be treated as positional, even
	// flag-looking tokens. A URI that starts with `--` is bizarre but the
	// terminator must still work.
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	// `--no-filter` after `--` should NOT set the flag; the peek should
	// be treated as a URI and fail with "not found".
	err := runPeek([]string{"--config", cfgPath, "--", "--no-filter"})
	require.Error(t, err)
	// Not an "unknown flag" error — the token reached the URI slot.
	assert.NotContains(t, err.Error(), "flag provided but not defined")
}

// ─────────────────── audit stdout → stderr redirect ───────────────────

func TestLoadDispatcherForCLI_RedirectsStdoutAudit(t *testing.T) {
	// A config with audit.mode: stdout would normally pollute stdout
	// with JSON audit lines. loadDispatcherForCLI must rewrite
	// mode → "stderr" so `pagefault load … | jq .` works.
	dir := t.TempDir()
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
		"filters:\n" +
		"  enabled: false\n" +
		"audit:\n" +
		"  enabled: true\n" +
		"  mode: \"stdout\"\n"
	cfgPath := filepath.Join(dir, "pagefault.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))

	// Run with --json so we can cleanly assert stdout contains only the
	// payload, not an audit line.
	out := captureStdout(t, func() {
		require.NoError(t, runMaps([]string{"--config", cfgPath, "--json"}))
	})
	// Stdout must be parseable as a single JSON object matching the
	// maps payload shape — no audit-line contamination.
	var decoded tool.ListContextsOutput
	require.NoError(t, json.Unmarshal([]byte(out), &decoded), "stdout should be clean payload, got: %q", out)
	require.NotContains(t, out, "caller_id", "audit line must not leak onto stdout")
}

// ─────────────────── fault (pf_fault) ───────────────────

// writeTestConfigWithSubagent is like writeTestConfig but also declares
// a subagent-cli backend whose "echo" command lets the CLI exercise
// pf_fault / pf_ps end-to-end without pulling in any external tool.
func writeTestConfigWithSubagent(t *testing.T, dir string) string {
	t.Helper()
	dataDir := filepath.Join(dir, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "README.md"), []byte("# hello\n"), 0o600))

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
		"  - name: sa\n" +
		"    type: subagent-cli\n" +
		"    command: \"echo {agent_id}:{task}\"\n" +
		"    timeout: 5\n" +
		// Passthrough templates so the echo command can assert against
		// the bare task string — the default memory-retrieval / memory-
		// write prompts would wrap the task in ~15 lines of framing
		// text and break these narrow CLI plumbing tests.
		"    retrieve_prompt_template: \"{task}\"\n" +
		"    write_prompt_template: \"{task}\"\n" +
		"    agents:\n" +
		"      - id: alpha\n" +
		"        description: \"primary\"\n" +
		"      - id: beta\n" +
		"        description: \"secondary\"\n" +
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
		"  pf_fault: true\n" +
		"  pf_ps: true\n" +
		"filters:\n" +
		"  enabled: false\n" +
		"audit:\n" +
		"  enabled: false\n"

	cfgPath := filepath.Join(dir, "pagefault.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))
	return cfgPath
}

func TestRunFault_Text(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfigWithSubagent(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runFault([]string{
			"--config", cfgPath,
			"--agent", "alpha",
			"--timeout", "5",
			"hello", "world",
		}))
	})
	// echo command renders "{agent_id}:{task}" → "alpha:hello world".
	assert.Contains(t, out, "alpha:hello world")
}

func TestRunFault_JSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfigWithSubagent(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runFault([]string{
			"--config", cfgPath, "--json", "question",
		}))
	})
	var decoded tool.DeepRetrieveOutput
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	assert.Equal(t, "alpha:question", decoded.Answer)
	assert.Equal(t, "alpha", decoded.Agent)
	assert.Equal(t, "sa", decoded.Backend)
	assert.False(t, decoded.TimedOut)
}

func TestRunFault_ExplicitAgent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfigWithSubagent(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runFault([]string{
			"--config", cfgPath,
			"--agent", "beta",
			"--json",
			"ping",
		}))
	})
	var decoded tool.DeepRetrieveOutput
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	assert.Equal(t, "beta:ping", decoded.Answer)
	assert.Equal(t, "beta", decoded.Agent)
}

func TestRunFault_MissingQuery(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfigWithSubagent(t, dir)

	err := runFault([]string{"--config", cfgPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage:")
}

func TestRunFault_NoSubagentConfigured(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir) // no subagent backend

	err := runFault([]string{"--config", cfgPath, "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent not found")
}

// ─────────────────── ps (pf_ps) ───────────────────

func TestRunPs_Text(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfigWithSubagent(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runPs([]string{"--config", cfgPath}))
	})
	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "BACKEND")
	assert.Contains(t, out, "alpha")
	assert.Contains(t, out, "beta")
	assert.Contains(t, out, "sa")
}

func TestRunPs_JSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfigWithSubagent(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runPs([]string{"--config", cfgPath, "--json"}))
	})
	var decoded tool.ListAgentsOutput
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.Len(t, decoded.Agents, 2)
	assert.Equal(t, "alpha", decoded.Agents[0].ID)
	assert.Equal(t, "sa", decoded.Agents[0].Backend)
}

func TestRunPs_Empty(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runPs([]string{"--config", cfgPath}))
	})
	assert.Contains(t, out, "no subagents configured")
}

// ─────────────────── poke (pf_poke) ───────────────────

// writeTestConfigWritable stages a config with a writable filesystem
// backend. The write_paths allowlist accepts any `memory://notes/*.md`
// URI.
func writeTestConfigWritable(t *testing.T, dir string) string {
	t.Helper()
	dataDir := filepath.Join(dir, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

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
		"    writable: true\n" +
		"    write_paths: [\"memory://notes/*.md\"]\n" +
		"    write_mode: \"append\"\n" +
		"    max_entry_size: 500\n" +
		"    file_locking: \"flock\"\n" +
		"contexts: []\n" +
		"tools:\n" +
		"  pf_poke: true\n" +
		"filters:\n" +
		"  enabled: false\n" +
		"audit:\n" +
		"  enabled: false\n"

	cfgPath := filepath.Join(dir, "pagefault.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))
	return cfgPath
}

func TestRunPoke_DirectText(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfigWritable(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runPoke([]string{
			"--config", cfgPath,
			"--mode", "direct",
			"--uri", "memory://notes/today.md",
			"hello", "from", "cli",
		}))
	})
	assert.Contains(t, out, "written memory://notes/today.md")
	assert.Contains(t, out, "backend=fs")

	got, err := os.ReadFile(filepath.Join(dir, "data/notes/today.md"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "hello from cli")
}

func TestRunPoke_DirectJSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfigWritable(t, dir)

	out := captureStdout(t, func() {
		require.NoError(t, runPoke([]string{
			"--config", cfgPath,
			"--mode", "direct",
			"--uri", "memory://notes/x.md",
			"--json",
			"json body",
		}))
	})
	var decoded tool.WriteOutput
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	assert.Equal(t, "written", decoded.Status)
	assert.Equal(t, "direct", decoded.Mode)
	assert.Equal(t, "memory://notes/x.md", decoded.URI)
	assert.Equal(t, "fs", decoded.Backend)
	assert.Positive(t, decoded.BytesWritten)
}

func TestRunPoke_MissingURIInDirectMode(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfigWritable(t, dir)

	err := runPoke([]string{
		"--config", cfgPath,
		"--mode", "direct",
		"body",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uri is required")
}

func TestRunPoke_NoContent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfigWritable(t, dir)

	// Empty content and empty stdin → usage error.
	withStdin(t, "", func() {
		err := runPoke([]string{
			"--config", cfgPath,
			"--mode", "direct",
			"--uri", "memory://notes/x.md",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "usage:")
	})
}

func TestRunPoke_ReadOnlyBackendRejected(t *testing.T) {
	dir := t.TempDir()
	// writeTestConfig sets up a read-only filesystem backend.
	cfgPath := writeTestConfig(t, dir)

	err := runPoke([]string{
		"--config", cfgPath,
		"--mode", "direct",
		"--uri", "memory://notes/x.md",
		"body",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
}

// withStdin swaps os.Stdin to a string-backed pipe for the duration
// of fn, then restores the original. Used by TestRunPoke_NoContent to
// exercise the "no positional args → read stdin" branch without
// blocking on a real terminal.
func withStdin(t *testing.T, content string, fn func()) {
	t.Helper()
	prev := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() {
		os.Stdin = prev
		_ = r.Close()
	}()
	os.Stdin = r
	if content != "" {
		_, _ = w.WriteString(content)
	}
	_ = w.Close()
	fn()
}

// ─────────────────── singleLine helper ───────────────────

func TestSingleLine(t *testing.T) {
	assert.Equal(t, "a b c", singleLine("a\nb\nc"))
	assert.Equal(t, "a b c", singleLine("a\tb\tc"))
	assert.Equal(t, "a", singleLine("  a\n"))
	assert.Equal(t, "", singleLine(""))
	// Mixed tabs and newlines collapse + trim.
	assert.Equal(t, "x y z", singleLine("x\ty\nz\n"))
}
