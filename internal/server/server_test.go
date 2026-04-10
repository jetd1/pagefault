package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/audit"
	"github.com/jet/pagefault/internal/auth"
	"github.com/jet/pagefault/internal/backend"
	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/filter"
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
	assert.Equal(t, "ok", backends["fs"])
}

func TestServer_Root_Landing(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := get(t, ts, "/", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "pagefault")
	assert.Contains(t, string(body), "/mcp")
}

// ───────────────── REST /api/list_contexts ─────────────────

func TestServer_ListContexts_NoAuth(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/list_contexts", nil, "")
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

// ───────────────── REST /api/get_context ─────────────────

func TestServer_GetContext(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/get_context", map[string]any{"name": "welcome"}, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "hello world")
}

func TestServer_GetContext_MissingName(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/get_context", map[string]any{}, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), "invalid request")
}

func TestServer_GetContext_UnknownName(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, _ := post(t, ts, "/api/get_context", map[string]any{"name": "nope"}, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ───────────────── REST /api/search ─────────────────

func TestServer_Search(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/search", map[string]any{"query": "hello"}, "")
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
	resp, _ := post(t, ts, "/api/search", map[string]any{"query": ""}, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ───────────────── REST /api/read ─────────────────

func TestServer_Read(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, body := post(t, ts, "/api/read", map[string]any{"uri": "memory://hello.md"}, "")
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
	resp, _ := post(t, ts, "/api/read", map[string]any{"uri": "memory://nope.md"}, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestServer_Read_UnknownScheme(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")
	resp, _ := post(t, ts, "/api/read", map[string]any{"uri": "other://x.md"}, "")
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
	resp, _ := post(t, ts, "/api/list_contexts", nil, "pf_test_secret")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestServer_Bearer_RejectsMissingToken(t *testing.T) {
	ts, _ := newTestServer(t, "bearer", writeTestTokens(t))
	resp, _ := post(t, ts, "/api/list_contexts", nil, "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestServer_Bearer_RejectsBadToken(t *testing.T) {
	ts, _ := newTestServer(t, "bearer", writeTestTokens(t))
	resp, _ := post(t, ts, "/api/list_contexts", nil, "wrong-token")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestServer_Bearer_HealthStillOpen(t *testing.T) {
	// /health must not require auth.
	ts, _ := newTestServer(t, "bearer", writeTestTokens(t))
	resp, _ := get(t, ts, "/health", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// ───────────────── MCP transport smoke ─────────────────

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
