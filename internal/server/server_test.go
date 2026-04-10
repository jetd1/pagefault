package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/audit"
	"github.com/jet/pagefault/internal/auth"
	"github.com/jet/pagefault/internal/backend"
	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/filter"
	"github.com/jet/pagefault/internal/model"
	"github.com/jet/pagefault/internal/tool"
)

// newTestServer spins up a full pagefault Server with a filesystem backend
// pointed at a temp directory seeded with one markdown file.
func newTestServer(t *testing.T, authMode string, tokensPath string) (*httptest.Server, string) {
	t.Helper()

	dir := t.TempDir()
	hello := filepath.Join(dir, "hello.md")
	require.NoError(t, os.WriteFile(hello, []byte("# hello\n\nhello world from pagefault\n"), 0o600))

	fsCfg := &config.FilesystemBackendConfig{
		Name:      "fs",
		Type:      "filesystem",
		Root:      dir,
		Include:   []string{"**/*.md"},
		URIScheme: "memory",
		Sandbox:   true,
	}
	fsBackend, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)

	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{fsBackend},
		Contexts: []config.ContextConfig{
			{
				Name:        "welcome",
				Description: "Welcome content",
				Sources:     []config.ContextSource{{Backend: "fs", URI: "memory://hello.md"}},
				Format:      "markdown",
				MaxSize:     10_000,
			},
		},
		Filter: filter.NewCompositeFilter(),
		Audit:  audit.NopLogger{},
	})
	require.NoError(t, err)

	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 0},
		Auth:   config.AuthConfig{Mode: authMode},
	}
	if authMode == "bearer" {
		cfg.Auth.Bearer.TokensFile = tokensPath
	}

	provider, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)

	srv, err := New(cfg, d, provider)
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts, dir
}

// post is a tiny helper for POST + JSON body + optional auth header.
func post(t *testing.T, ts *httptest.Server, path string, body any, token string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, data
}

// get is a helper for GET requests.
func get(t *testing.T, ts *httptest.Server, path string, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, data
}

// ───────────────── Health & Root ─────────────────

func TestServer_Health_NoAuthRequired(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := get(t, ts, "/health", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "response code")
	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, "ok", out["status"])
	backends := out["backends"].(map[string]any)
	fs := backends["fs"].(map[string]any)
	assert.Equal(t, "ok", fs["status"])
	// A healthy filesystem backend should not have an error field.
	_, hasErr := fs["error"]
	assert.False(t, hasErr)
}

func TestServer_Health_DegradedWhenBackendDown(t *testing.T) {
	// Build a server whose filesystem backend root is deleted after
	// startup — Health should return "unavailable" for that backend and
	// "degraded" overall. /health still returns 200 so operators can
	// fetch it cheaply.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.md"), []byte("hi"), 0o600))

	fsCfg := &config.FilesystemBackendConfig{
		Name: "fs", Type: "filesystem", Root: dir,
		Include: []string{"**/*.md"}, URIScheme: "memory", Sandbox: true,
	}
	fsBackend, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)

	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{fsBackend},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 0},
		Auth:   config.AuthConfig{Mode: "none"},
	}
	p, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)
	srv, err := New(cfg, d, p)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	// Replace the root with a regular file so Health's stat call sees a
	// non-directory and fails. os.Remove+create keeps the same path.
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.WriteFile(dir, []byte("not a dir"), 0o600))
	defer os.Remove(dir)

	resp, body := get(t, ts, "/health", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "health should always return 200")

	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	// Only one backend and it's down → overall "unavailable".
	assert.Equal(t, "unavailable", out["status"])

	backends := out["backends"].(map[string]any)
	fs := backends["fs"].(map[string]any)
	assert.Equal(t, "unavailable", fs["status"])
	assert.NotEmpty(t, fs["error"])
}

func TestServer_Root_Landing(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := get(t, ts, "/", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "pagefault")
	assert.Contains(t, string(body), "/mcp")
}

// ───────────────── REST /api/pf_maps ─────────────────

func TestServer_ListContexts_NoAuth(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/pf_maps", nil, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Contexts []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"contexts"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.Len(t, out.Contexts, 1)
	assert.Equal(t, "welcome", out.Contexts[0].Name)
}

// ───────────────── REST /api/pf_load ─────────────────

func TestServer_GetContext(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/pf_load", map[string]any{"name": "welcome"}, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "hello world")
}

func TestServer_GetContext_MissingName(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/pf_load", map[string]any{}, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var env struct {
		Error struct {
			Code    string `json:"code"`
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &env))
	assert.Equal(t, "invalid_request", env.Error.Code)
	assert.Equal(t, http.StatusBadRequest, env.Error.Status)
	assert.Contains(t, env.Error.Message, "name is required")
}

func TestServer_Read_Missing_StructuredError(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/pf_peek", map[string]any{"uri": "memory://nope.md"}, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	var env struct {
		Error struct {
			Code    string `json:"code"`
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &env))
	assert.Equal(t, "resource_not_found", env.Error.Code)
	assert.Equal(t, http.StatusNotFound, env.Error.Status)
}

func TestServer_GetContext_UnknownName(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, _ := post(t, ts, "/api/pf_load", map[string]any{"name": "nope"}, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ───────────────── REST /api/pf_scan ─────────────────

func TestServer_Search(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/pf_scan", map[string]any{"query": "hello"}, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Results []struct {
			URI     string `json:"uri"`
			Backend string `json:"backend"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.NotEmpty(t, out.Results)
	assert.Equal(t, "memory://hello.md", out.Results[0].URI)
	assert.Equal(t, "fs", out.Results[0].Backend)
}

func TestServer_Search_EmptyQuery(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, _ := post(t, ts, "/api/pf_scan", map[string]any{"query": ""}, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ───────────────── REST /api/pf_peek ─────────────────

func TestServer_Read(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/pf_peek", map[string]any{"uri": "memory://hello.md"}, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Resource *backend.Resource `json:"resource"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.NotNil(t, out.Resource)
	assert.Contains(t, out.Resource.Content, "hello world")
	assert.Equal(t, "text/markdown", out.Resource.ContentType)
}

func TestServer_Read_Missing(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, _ := post(t, ts, "/api/pf_peek", map[string]any{"uri": "memory://nope.md"}, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestServer_Read_UnknownScheme(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, _ := post(t, ts, "/api/pf_peek", map[string]any{"uri": "other://x.md"}, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ───────────────── Bearer auth ─────────────────

func writeTestTokens(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.jsonl")
	line := `{"id":"test","token":"pf_test_secret","label":"Test Token"}`
	require.NoError(t, os.WriteFile(path, []byte(line), 0o600))
	return path
}

func TestServer_Bearer_AllowsValidToken(t *testing.T) {
	ts, _ := newTestServer(t, "bearer", writeTestTokens(t))
	resp, _ := post(t, ts, "/api/pf_maps", nil, "pf_test_secret")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestServer_Bearer_RejectsMissingToken(t *testing.T) {
	ts, _ := newTestServer(t, "bearer", writeTestTokens(t))
	resp, _ := post(t, ts, "/api/pf_maps", nil, "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestServer_Bearer_RejectsBadToken(t *testing.T) {
	ts, _ := newTestServer(t, "bearer", writeTestTokens(t))
	resp, _ := post(t, ts, "/api/pf_maps", nil, "wrong-token")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestServer_Bearer_HealthStillOpen(t *testing.T) {
	// /health must not require auth.
	ts, _ := newTestServer(t, "bearer", writeTestTokens(t))
	resp, _ := get(t, ts, "/health", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// ───────────────── OpenAPI spec ─────────────────

func TestServer_OpenAPISpec_NoAuth(t *testing.T) {
	ts, _ := newTestServer(t, "bearer", writeTestTokens(t))
	// Should be reachable without a token — ChatGPT Actions imports it
	// before the user pastes the bearer credential.
	resp, body := get(t, ts, "/api/openapi.json", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var spec map[string]any
	require.NoError(t, json.Unmarshal(body, &spec))
	assert.Equal(t, "3.1.0", spec["openapi"])

	info := spec["info"].(map[string]any)
	assert.Equal(t, "pagefault", info["title"])

	paths := spec["paths"].(map[string]any)
	for _, want := range []string{"/api/pf_maps", "/api/pf_load", "/api/pf_scan", "/api/pf_peek"} {
		_, ok := paths[want]
		assert.True(t, ok, "missing path %s", want)
	}

	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	_, hasEnvelope := schemas["ErrorEnvelope"]
	assert.True(t, hasEnvelope, "components.schemas.ErrorEnvelope missing")
}

func TestServer_OpenAPISpec_RespectsDisabledTools(t *testing.T) {
	// Manually build a dispatcher with pf_peek disabled so we can assert
	// the spec drops its path entry.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.md"), []byte("hi"), 0o600))

	fsCfg := &config.FilesystemBackendConfig{
		Name: "fs", Type: "filesystem", Root: dir,
		Include: []string{"**/*.md"}, URIScheme: "memory", Sandbox: true,
	}
	fsBackend, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)

	f := false
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{fsBackend},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
		Tools:    config.ToolsConfig{PfPeek: &f},
	})
	require.NoError(t, err)

	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 0},
		Auth:   config.AuthConfig{Mode: "none"},
	}
	p, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)
	srv, err := New(cfg, d, p)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)

	resp, body := get(t, ts, "/api/openapi.json", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var spec map[string]any
	require.NoError(t, json.Unmarshal(body, &spec))
	paths := spec["paths"].(map[string]any)
	_, hasPeek := paths["/api/pf_peek"]
	assert.False(t, hasPeek, "pf_peek should not appear in spec when disabled")
}

// ───────────────── MCP transport smoke ─────────────────

// TestErrorCodeMapping covers the error-sentinel → stable-code mapping
// that both writeError and writeAuthError lean on. The rate_limited entry
// was added in 0.4.1 so any caller that wraps model.ErrRateLimited and
// hands it to writeError gets the same envelope the rate-limit middleware
// emits — guard it with a direct table test so a future typo can't
// silently diverge the two paths.
func TestErrorCodeMapping(t *testing.T) {
	cases := []struct {
		err        error
		wantCode   string
		wantStatus int
	}{
		{model.ErrInvalidRequest, "invalid_request", http.StatusBadRequest},
		{model.ErrUnauthenticated, "unauthenticated", http.StatusUnauthorized},
		{model.ErrAccessViolation, "access_violation", http.StatusForbidden},
		{model.ErrResourceNotFound, "resource_not_found", http.StatusNotFound},
		{model.ErrContextNotFound, "context_not_found", http.StatusNotFound},
		{model.ErrBackendNotFound, "backend_not_found", http.StatusNotFound},
		{model.ErrAgentNotFound, "agent_not_found", http.StatusNotFound},
		{model.ErrBackendUnavailable, "backend_unavailable", http.StatusBadGateway},
		{model.ErrSubagentTimeout, "subagent_timeout", http.StatusGatewayTimeout},
		{model.ErrRateLimited, "rate_limited", http.StatusTooManyRequests},
		{model.ErrContentTooLarge, "content_too_large", http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.wantCode, errorCode(tc.err), "code for %v", tc.err)
		assert.Equal(t, tc.wantStatus, errorStatus(tc.err), "status for %v", tc.err)
	}
}

// ───────────────── pf_poke REST tests ─────────────────

// newWritableTestServer spins up a full pagefault Server with a
// writable filesystem backend. Used by the pf_poke REST tests.
func newWritableTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	fsCfg := &config.FilesystemBackendConfig{
		Name:         "fs",
		Type:         "filesystem",
		Root:         dir,
		Include:      []string{"**/*.md"},
		URIScheme:    "memory",
		Sandbox:      true,
		Writable:     true,
		WritePaths:   []string{"memory://notes/*.md"},
		WriteMode:    "append",
		MaxEntrySize: 500,
		FileLocking:  "flock",
	}
	fsBackend, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)

	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{fsBackend},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 0},
		Auth:   config.AuthConfig{Mode: "none"},
	}
	provider, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)
	srv, err := New(cfg, d, provider)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts, dir
}

func TestServer_Write_Direct(t *testing.T) {
	ts, dir := newWritableTestServer(t)
	resp, body := post(t, ts, "/api/pf_poke", map[string]any{
		"uri":     "memory://notes/x.md",
		"content": "content from rest",
		"mode":    "direct",
	}, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))

	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, "written", out["status"])
	assert.Equal(t, "direct", out["mode"])
	assert.Equal(t, "memory://notes/x.md", out["uri"])

	// Verify the file contents.
	got, err := os.ReadFile(filepath.Join(dir, "notes/x.md"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "content from rest")
}

func TestServer_Write_DirectMissingContent(t *testing.T) {
	ts, _ := newWritableTestServer(t)
	resp, body := post(t, ts, "/api/pf_poke", map[string]any{
		"uri":  "memory://notes/x.md",
		"mode": "direct",
	}, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var env map[string]any
	require.NoError(t, json.Unmarshal(body, &env))
	errObj := env["error"].(map[string]any)
	assert.Equal(t, "invalid_request", errObj["code"])
}

func TestServer_Write_ContentTooLarge(t *testing.T) {
	ts, _ := newWritableTestServer(t)

	// Feed enough content to exceed the 500-byte max_entry_size.
	big := make([]byte, 600)
	for i := range big {
		big[i] = 'x'
	}
	resp, body := post(t, ts, "/api/pf_poke", map[string]any{
		"uri":     "memory://notes/big.md",
		"content": string(big),
		"mode":    "direct",
	}, "")
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode, "body=%s", string(body))

	var env map[string]any
	require.NoError(t, json.Unmarshal(body, &env))
	errObj := env["error"].(map[string]any)
	assert.Equal(t, "content_too_large", errObj["code"])
}

func TestServer_Write_ReadOnlyBackend(t *testing.T) {
	// Default newTestServer uses a read-only backend — pf_poke should
	// return 403 access_violation.
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/pf_poke", map[string]any{
		"uri":     "memory://notes/x.md",
		"content": "x",
		"mode":    "direct",
	}, "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "body=%s", string(body))

	var env map[string]any
	require.NoError(t, json.Unmarshal(body, &env))
	errObj := env["error"].(map[string]any)
	assert.Equal(t, "access_violation", errObj["code"])
}

func TestServer_OpenAPISpec_IncludesPoke(t *testing.T) {
	ts, _ := newWritableTestServer(t)
	resp, body := get(t, ts, "/api/openapi.json", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var spec map[string]any
	require.NoError(t, json.Unmarshal(body, &spec))
	paths := spec["paths"].(map[string]any)
	_, ok := paths["/api/pf_poke"]
	assert.True(t, ok, "pf_poke path should be in the OpenAPI spec")

	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	_, hasIn := schemas["WriteInput"]
	_, hasOut := schemas["WriteOutput"]
	assert.True(t, hasIn, "WriteInput schema should be defined")
	assert.True(t, hasOut, "WriteOutput schema should be defined")
}

func TestServer_Root_Landing_MentionsPoke(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := get(t, ts, "/", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "/api/pf_poke")
}

// TestServer_MCP_Initialize verifies the /mcp endpoint accepts the MCP
// initialize request. This is a smoke test — it proves the mcp-go handler is
// correctly mounted and speaks JSON-RPC. A full MCP client integration test
// lives in mcptest, which we could add in a later phase.
func TestServer_MCP_Initialize(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		},
	}
	b, _ := json.Marshal(initReq)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "initialize should 200, got body=%s", string(data))
	assert.Contains(t, string(data), "result", "response should contain a JSON-RPC result field")
}

// TestServer_MCP_InstructionsInInitialize confirms the default
// instructions text from internal/tool.DefaultInstructions is advertised
// in the streamable-http initialize response. MCP clients like Claude
// Desktop surface this in the agent's system prompt, so it is the
// single most important lever for steering agents toward pf_* tools.
func TestServer_MCP_InstructionsInInitialize(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	body := doInitializeOverStreamable(t, ts)
	// A distinctive phrase from DefaultInstructions — if this ever fails
	// because the text was edited, update the assertion to match.
	assert.Contains(t, body, "pagefault is the user's personal memory server")
	assert.Contains(t, body, "pf_scan")
}

// TestServer_MCP_InstructionsOverride verifies that a non-empty
// server.mcp.instructions config replaces the built-in default verbatim.
func TestServer_MCP_InstructionsOverride(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.md"), []byte("hi"), 0o600))
	fsCfg := &config.FilesystemBackendConfig{
		Name: "fs", Type: "filesystem", Root: dir,
		Include: []string{"**/*.md"}, URIScheme: "memory", Sandbox: true,
	}
	fsBackend, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{fsBackend},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1", Port: 0,
			MCP: config.MCPConfig{Instructions: "custom-instructions-sentinel-xyz"},
		},
		Auth: config.AuthConfig{Mode: "none"},
	}
	p, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)
	srv, err := New(cfg, d, p)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)

	body := doInitializeOverStreamable(t, ts)
	assert.Contains(t, body, "custom-instructions-sentinel-xyz")
	// The default text must not leak through when an override is set.
	assert.NotContains(t, body, "pagefault is the user's personal memory server")
}

// doInitializeOverStreamable issues an MCP initialize request against
// /mcp and returns the response body as a string. Streamable-http
// responses are already in SSE framing, so the body contains the
// JSON-RPC envelope in a `data:` line — contains-checks are reliable.
func doInitializeOverStreamable(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		},
	}
	b, _ := json.Marshal(initReq)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", string(data))
	return string(data)
}

// ───────────────── MCP legacy-SSE transport ─────────────────

// TestServer_SSE_Handshake verifies that GET /sse returns a persistent
// SSE stream whose first event is the "endpoint" event carrying a
// sessionId query parameter. This is the bit Claude Desktop and other
// SSE-only clients expect before they start POSTing messages.
func TestServer_SSE_Handshake(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	reader := bufio.NewReader(resp.Body)
	event := readSSEEvent(t, reader)
	assert.Contains(t, event, "event: endpoint",
		"first SSE event must announce the message endpoint")
	assert.Contains(t, event, "sessionId=",
		"endpoint URL must include a sessionId parameter")
}

// TestServer_SSE_InitializeRoundtrip drives a full MCP initialize
// handshake through the legacy-SSE transport:
//
//  1. GET /sse → receive the endpoint event with sessionId.
//  2. POST /message?sessionId=... with the initialize request.
//  3. Read the next SSE event → it is the initialize result.
//
// This is the flow Claude Desktop runs every time it connects, so a
// regression here would break the primary reason the SSE transport
// exists. We also assert the instructions string surfaces in the
// result so the two features (SSE + instructions) are tested
// end-to-end together.
func TestServer_SSE_InitializeRoundtrip(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Step 1 — open the SSE stream.
	sseReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/sse", nil)
	sseResp, err := http.DefaultClient.Do(sseReq)
	require.NoError(t, err)
	defer sseResp.Body.Close()
	require.Equal(t, http.StatusOK, sseResp.StatusCode)

	reader := bufio.NewReader(sseResp.Body)

	// Step 2 — parse the endpoint event, extract the message path.
	endpoint := readSSEEvent(t, reader)
	require.Contains(t, endpoint, "event: endpoint")
	messagePath := extractSSEData(t, endpoint)
	require.NotEmpty(t, messagePath, "endpoint event data must not be empty")
	require.Contains(t, messagePath, "sessionId=")
	// In the no-public_url case mcp-go emits a root-relative URL —
	// prepend the httptest server URL to reach it.
	if strings.HasPrefix(messagePath, "/") {
		messagePath = ts.URL + messagePath
	}

	// Step 3 — POST the initialize request to the message endpoint.
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "sse-test", "version": "0"},
		},
	}
	body, _ := json.Marshal(initReq)
	postReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, messagePath, bytes.NewReader(body))
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(postReq)
	require.NoError(t, err)
	defer postResp.Body.Close()
	require.Equal(t, http.StatusAccepted, postResp.StatusCode,
		"message endpoint should 202-Accept")

	// Step 4 — read the next SSE event; it carries the JSON-RPC result.
	respEvent := readSSEEvent(t, reader)
	assert.Contains(t, respEvent, "event: message")
	assert.Contains(t, respEvent, `"result"`)
	// Instructions must flow through to the initialize result regardless
	// of which transport the client used.
	assert.Contains(t, respEvent, "pagefault is the user's personal memory server")
}

// TestServer_SSE_DisabledReturns404 verifies that explicitly setting
// server.mcp.sse_enabled: false removes the /sse route entirely, so
// operators who only want streamable-http can shrink the public
// surface.
func TestServer_SSE_DisabledReturns404(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.md"), []byte("hi"), 0o600))
	fsCfg := &config.FilesystemBackendConfig{
		Name: "fs", Type: "filesystem", Root: dir,
		Include: []string{"**/*.md"}, URIScheme: "memory", Sandbox: true,
	}
	fsBackend, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{fsBackend},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	disabled := false
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1", Port: 0,
			MCP: config.MCPConfig{SSEEnabled: &disabled},
		},
		Auth: config.AuthConfig{Mode: "none"},
	}
	p, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)
	srv, err := New(cfg, d, p)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, _ := get(t, ts, "/sse", "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// /mcp should still be reachable — disabling SSE must not affect
	// the streamable-http transport.
	initBody := doInitializeOverStreamable(t, ts)
	assert.Contains(t, initBody, "result")
}

// TestServer_SSE_Disabled_RootLandingHidesIt checks that / only mentions
// /sse when the SSE transport is actually enabled.
func TestServer_SSE_Disabled_RootLandingHidesIt(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.md"), []byte("hi"), 0o600))
	fsCfg := &config.FilesystemBackendConfig{
		Name: "fs", Type: "filesystem", Root: dir,
		Include: []string{"**/*.md"}, URIScheme: "memory", Sandbox: true,
	}
	fsBackend, err := backend.NewFilesystemBackend(fsCfg)
	require.NoError(t, err)
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{fsBackend},
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	disabled := false
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1", Port: 0,
			MCP: config.MCPConfig{SSEEnabled: &disabled},
		},
		Auth: config.AuthConfig{Mode: "none"},
	}
	p, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)
	srv, err := New(cfg, d, p)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, body := get(t, ts, "/", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(body), "/sse")
	assert.NotContains(t, string(body), "/message")
}

// TestDefaultInstructionsNotEmpty is a belt-and-braces guard that the
// default instructions constant has not been accidentally blanked out,
// which would silently fall back to mcp-go's empty default and leave
// agents without any guidance on when to use pf_* tools.
func TestDefaultInstructionsNotEmpty(t *testing.T) {
	assert.NotEmpty(t, tool.DefaultInstructions,
		"tool.DefaultInstructions must not be empty — agents rely on it")
	assert.Contains(t, tool.DefaultInstructions, "pf_scan",
		"default instructions should at minimum mention pf_scan")
}

// TestDefaultInstructions_MultiAgentRouting guards the fix for the
// trace-observed friction where a calling agent skipped pf_ps and
// defaulted pf_fault to the first configured subagent. The default
// instructions must tell agents to call pf_ps first in multi-agent
// setups — if a future edit drops that guidance, the fallback
// silently re-introduces the wrong-scope-agent bug.
func TestDefaultInstructions_MultiAgentRouting(t *testing.T) {
	t.Run("mentions pf_ps as a routing step", func(t *testing.T) {
		assert.Contains(t, tool.DefaultInstructions, "pf_ps",
			"default instructions must mention pf_ps by name")
	})
	t.Run("has a multi-agent routing section", func(t *testing.T) {
		assert.Contains(t, tool.DefaultInstructions, "Multi-agent",
			"default instructions should have a Multi-agent routing section so agents know to call pf_ps before pf_fault/pf_poke")
	})
	t.Run("warns against the silent first-agent fallback", func(t *testing.T) {
		// Any of these phrasings prove the point; require at least one.
		text := tool.DefaultInstructions
		hasFallbackWarning := strings.Contains(text, "do not rely") ||
			strings.Contains(text, "do not default") ||
			strings.Contains(text, "first configured")
		assert.True(t, hasFallbackWarning,
			"default instructions should warn against the \"first configured agent\" fallback")
	})
	t.Run("sets a timeout floor", func(t *testing.T) {
		// Spell out the 120s floor somewhere in the text so an agent
		// reading the system prompt sees the real-latency guidance.
		assert.Contains(t, tool.DefaultInstructions, "120",
			"default instructions should mention the 120s timeout floor")
	})
}

// TestDefaultInstructions_ChatHistoryFraming guards the fix for the
// trace-observed friction where agents did not reach for pagefault on
// "what did we talk about" questions, because nothing told them
// pagefault's backends commonly include past-conversation archives.
// Without this framing, Claude defaults to searching its own context
// window and says "I don't remember" when really the answer is one
// pf_fault call away.
func TestDefaultInstructions_ChatHistoryFraming(t *testing.T) {
	text := tool.DefaultInstructions

	t.Run("mentions past-conversation archives in the intro", func(t *testing.T) {
		hasConversationFraming := strings.Contains(text, "past conversations") ||
			strings.Contains(text, "past conversation") ||
			strings.Contains(text, "chat history") ||
			strings.Contains(text, "past chat")
		assert.True(t, hasConversationFraming,
			"default instructions should explicitly mention that pagefault stores past conversations / chat history — otherwise agents won't route 'what did we talk about' questions here")
	})

	t.Run("mentions a concrete chat-archive backend as an example", func(t *testing.T) {
		// Naming a real backend (lossless-lcm, transcripts, embedding
		// indices) grounds the claim. A vague "may include chat history"
		// is less convincing than a concrete example.
		hasConcreteExample := strings.Contains(text, "lossless-lcm") ||
			strings.Contains(text, "transcripts") ||
			strings.Contains(text, "embedding")
		assert.True(t, hasConcreteExample,
			"default instructions should name at least one concrete chat-archive mechanism so the claim feels grounded")
	})
}

// TestDefaultInstructions_NoFalseNoMemoryClaim guards the hard rule
// that agents must not claim "I don't remember" without first calling
// pf_scan or pf_fault. This is the highest-leverage lever we have to
// prevent the "Claude answers from in-context memory and gives up"
// failure mode.
func TestDefaultInstructions_NoFalseNoMemoryClaim(t *testing.T) {
	text := tool.DefaultInstructions

	t.Run("has an explicit do-not-say-no-memory rule", func(t *testing.T) {
		// Match any of several plausible phrasings.
		hasRule := strings.Contains(text, "I don't remember") ||
			strings.Contains(text, "don't remember") ||
			strings.Contains(text, "no record") ||
			strings.Contains(text, "no memory")
		assert.True(t, hasRule,
			"default instructions should explicitly forbid the \"I don't remember\" answer without a pagefault check")
	})

	t.Run("routes the rule under a prominent section heading", func(t *testing.T) {
		// A "## Core rule" or equivalent heading makes the rule hard to
		// miss. Without a heading the text tends to get skimmed.
		hasCoreRuleHeading := strings.Contains(text, "## Core rule") ||
			strings.Contains(text, "## Core") ||
			strings.Contains(text, "## Must") ||
			strings.Contains(text, "## Never")
		assert.True(t, hasCoreRuleHeading,
			"default instructions should route the no-false-memory rule under a prominent section heading so agents don't skim past it")
	})
}

// TestDefaultInstructions_CrossLanguageSignalPhrases guards the fix
// for the trace-observed friction where Chinese queries like
// "我三月在干嘛" / "我4月2号做了些什么" did not pattern-match any of
// the English-only signal phrases in the original instructions, so
// Claude never routed them to pagefault. At least one zh-CN signal
// phrase must survive future edits to prove the cross-language
// coverage is intentional and not accidental.
func TestDefaultInstructions_CrossLanguageSignalPhrases(t *testing.T) {
	text := tool.DefaultInstructions

	t.Run("contains at least one zh signal phrase", func(t *testing.T) {
		// Any of these prove coverage; we don't pin a specific phrase
		// so the instructions can evolve without churning the test.
		hasZhSignal := strings.Contains(text, "我三月在干嘛") ||
			strings.Contains(text, "我做了什么") ||
			strings.Contains(text, "我跟你说过") ||
			strings.Contains(text, "我最近") ||
			strings.Contains(text, "记一下") ||
			strings.Contains(text, "我之前")
		assert.True(t, hasZhSignal,
			"default instructions should contain at least one Chinese signal phrase so zh-CN users' questions route to pagefault")
	})

	t.Run("contains at least one English signal phrase", func(t *testing.T) {
		// Belt and braces — the English side is what the majority of
		// sessions rely on.
		hasEnSignal := strings.Contains(text, "What did I note") ||
			strings.Contains(text, "what did we talk about") ||
			strings.Contains(text, "where did I write")
		assert.True(t, hasEnSignal,
			"default instructions should still contain English signal phrases — the cross-language addition must not displace them")
	})

	t.Run("mentions temporal-reference routing", func(t *testing.T) {
		// Any question with a past-time marker should route to
		// pagefault; call that out explicitly.
		assert.Contains(t, text, "Temporal",
			"default instructions should have a Temporal references section so agents know past-time markers are a pagefault signal")
	})
}

// readSSEEvent reads from an SSE stream until it sees a blank line
// (the SSE event terminator) and returns the accumulated event text,
// including trailing newline. An error or EOF fails the test — SSE
// streams should never end mid-event in these tests.
func readSSEEvent(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var sb strings.Builder
	for {
		line, err := r.ReadString('\n')
		require.NoErrorf(t, err, "SSE read: %v", err)
		sb.WriteString(line)
		// An SSE event terminates with a blank line. mcp-go writes
		// "\r\n\r\n" for the endpoint event and "\n\n" for subsequent
		// ones, so accept either flavour.
		if line == "\n" || line == "\r\n" {
			return sb.String()
		}
	}
}

// extractSSEData pulls the `data: ...` payload out of an SSE event
// string. Returns the trimmed data content (everything after "data: "
// on that line, with trailing CRLF stripped).
func extractSSEData(t *testing.T, event string) string {
	t.Helper()
	for _, line := range strings.Split(event, "\n") {
		if after, ok := strings.CutPrefix(line, "data: "); ok {
			return strings.TrimRight(after, "\r")
		}
		// mcp-go's keep-alive ping omits the space between "data:" and
		// the payload, so accept the tighter form too.
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			return strings.TrimRight(after, "\r")
		}
	}
	t.Fatalf("no data: line in SSE event: %q", event)
	return ""
}
