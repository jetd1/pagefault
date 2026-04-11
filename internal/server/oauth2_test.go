package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

// newOAuth2TestServer spins up a full pagefault Server wired to the
// OAuth2 provider, seeded with a single client whose credentials are
// returned alongside the httptest server. The caller uses the
// credentials against /oauth/token to obtain an access token and then
// hits /api/pf_* with the Bearer header.
func newOAuth2TestServer(t *testing.T, extraBearerTokenFile string) (ts *httptest.Server, clientID, clientSecret string) {
	t.Helper()

	dir := t.TempDir()
	hello := filepath.Join(dir, "hello.md")
	require.NoError(t, os.WriteFile(hello, []byte("# hello\n\nhello world\n"), 0o600))

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
		Filter:   filter.NewCompositeFilter(),
		Audit:    audit.NopLogger{},
	})
	require.NoError(t, err)

	// Seed a clients file with one known client.
	clientID = "claude-desktop"
	clientSecret = "pf_cs_testsecret"
	clientsPath := filepath.Join(dir, "oauth-clients.jsonl")
	hash, err := auth.HashClientSecret(clientSecret)
	require.NoError(t, err)
	rec := auth.ClientRecord{
		ID:         clientID,
		Label:      "Claude Desktop",
		SecretHash: hash,
		Scopes:     []string{"mcp"},
	}
	f, err := os.Create(clientsPath)
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(f).Encode(&rec))
	require.NoError(t, f.Close())

	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 0},
		Auth: config.AuthConfig{
			Mode: "oauth2",
			OAuth2: config.OAuth2Config{
				ClientsFile:           clientsPath,
				AccessTokenTTLSeconds: 3600,
				DefaultScopes:         []string{"mcp"},
			},
		},
	}
	if extraBearerTokenFile != "" {
		cfg.Auth.Bearer.TokensFile = extraBearerTokenFile
	}

	provider, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)

	srv, err := New(cfg, d, provider)
	require.NoError(t, err)

	ts = httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts, clientID, clientSecret
}

func TestOAuth2_Discovery_ProtectedResource(t *testing.T) {
	ts, _, _ := newOAuth2TestServer(t, "")

	resp, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(t, body, "resource")
	assert.Contains(t, body, "authorization_servers")
	servers, _ := body["authorization_servers"].([]any)
	require.NotEmpty(t, servers)
	assert.Equal(t, "pagefault", body["resource_name"])
}

func TestOAuth2_Discovery_AuthorizationServer(t *testing.T) {
	ts, _, _ := newOAuth2TestServer(t, "")

	resp, err := http.Get(ts.URL + "/.well-known/oauth-authorization-server")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	assert.Contains(t, body, "issuer")
	tokEndpoint, ok := body["token_endpoint"].(string)
	require.True(t, ok)
	assert.Contains(t, tokEndpoint, "/oauth/token")

	grants, _ := body["grant_types_supported"].([]any)
	require.NotEmpty(t, grants)
	assert.Equal(t, "client_credentials", grants[0].(string))

	methods, _ := body["token_endpoint_auth_methods_supported"].([]any)
	require.Len(t, methods, 2)
}

// TestOAuth2_Token_BasicAuth exercises the client_secret_basic path,
// which is what Claude Desktop's native SSE config uses.
func TestOAuth2_Token_BasicAuth(t *testing.T) {
	ts, clientID, clientSecret := newOAuth2TestServer(t, "")

	form := url.Values{}
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/oauth/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "body: %s", body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	var tokenResp map[string]any
	require.NoError(t, json.Unmarshal(body, &tokenResp))
	assert.Equal(t, "Bearer", tokenResp["token_type"])
	assert.NotEmpty(t, tokenResp["access_token"])
	assert.True(t, strings.HasPrefix(tokenResp["access_token"].(string), "pf_at_"))
	// expires_in should be positive.
	if ex, ok := tokenResp["expires_in"].(float64); ok {
		assert.True(t, ex > 0)
	}
}

// TestOAuth2_Token_FormBody exercises the client_secret_post path.
func TestOAuth2_Token_FormBody(t *testing.T) {
	ts, clientID, clientSecret := newOAuth2TestServer(t, "")

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)

	resp, err := http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var tokenResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tokenResp))
	assert.NotEmpty(t, tokenResp["access_token"])
}

func TestOAuth2_Token_InvalidSecret_Basic(t *testing.T) {
	ts, clientID, _ := newOAuth2TestServer(t, "")

	form := url.Values{}
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/oauth/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, "wrong-secret")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	// Per RFC 6749 §5.2, Basic-auth credential failures → 401 + WWW-Authenticate.
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"), "Basic")

	var errBody map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errBody))
	assert.Equal(t, "invalid_client", errBody["error"])
}

func TestOAuth2_Token_InvalidSecret_Post(t *testing.T) {
	ts, clientID, _ := newOAuth2TestServer(t, "")

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", "wrong")

	resp, err := http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()
	// Form-body credential failures → 400 (no WWW-Authenticate).
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var errBody map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errBody))
	assert.Equal(t, "invalid_client", errBody["error"])
}

func TestOAuth2_Token_UnsupportedGrant(t *testing.T) {
	ts, _, _ := newOAuth2TestServer(t, "")

	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("username", "x")
	form.Set("password", "y")

	resp, err := http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var errBody map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errBody))
	assert.Equal(t, "unsupported_grant_type", errBody["error"])
}

// TestOAuth2_Token_GrantTypeInQueryRejected verifies strict RFC 6749
// §4.4 behaviour: grant_type MUST arrive in the
// application/x-www-form-urlencoded POST body, not the URL query
// string. A client that passes ?grant_type=client_credentials gets
// unsupported_grant_type (even though the body is otherwise valid),
// so the bug is visible in the client's logs.
func TestOAuth2_Token_GrantTypeInQueryRejected(t *testing.T) {
	ts, clientID, clientSecret := newOAuth2TestServer(t, "")

	// grant_type only in the query string, body is empty.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/oauth/token?grant_type=client_credentials", strings.NewReader(""))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var errBody map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errBody))
	assert.Equal(t, "unsupported_grant_type", errBody["error"])
}

func TestOAuth2_Token_MissingCredentials(t *testing.T) {
	ts, _, _ := newOAuth2TestServer(t, "")

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	resp, err := http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"), "Basic")
}

// TestOAuth2_EndToEnd_UseIssuedTokenOnAPI goes through the full
// dance: hit /oauth/token to get an access_token, then use that
// token as a bearer on /api/pf_maps and confirm the call succeeds.
func TestOAuth2_EndToEnd_UseIssuedTokenOnAPI(t *testing.T) {
	ts, clientID, clientSecret := newOAuth2TestServer(t, "")

	// Step 1: exchange credentials for an access token.
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/oauth/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var tokenResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tokenResp))
	accessToken := tokenResp["access_token"].(string)
	require.NotEmpty(t, accessToken)

	// Step 2: use the access token on a protected API route.
	apiReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/pf_maps", strings.NewReader("{}"))
	require.NoError(t, err)
	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("Authorization", "Bearer "+accessToken)
	apiResp, err := http.DefaultClient.Do(apiReq)
	require.NoError(t, err)
	defer apiResp.Body.Close()
	require.Equal(t, http.StatusOK, apiResp.StatusCode)
}

// TestOAuth2_CompoundMode_LegacyBearerStillWorks verifies the
// migration-friendly path: when both OAuth2 and bearer tokens_file
// are configured, a long-lived static bearer token continues to work.
func TestOAuth2_CompoundMode_LegacyBearerStillWorks(t *testing.T) {
	dir := t.TempDir()
	tokensPath := filepath.Join(dir, "tokens.jsonl")
	legacy := "pf_legacy_compound_token"
	jsonl := `{"id":"legacy","token":"` + legacy + `","label":"Legacy"}` + "\n"
	require.NoError(t, os.WriteFile(tokensPath, []byte(jsonl), 0o600))

	ts, clientID, clientSecret := newOAuth2TestServer(t, tokensPath)

	// 1: Legacy bearer token works straight against the API.
	apiReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/pf_maps", strings.NewReader("{}"))
	require.NoError(t, err)
	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("Authorization", "Bearer "+legacy)
	apiResp, err := http.DefaultClient.Do(apiReq)
	require.NoError(t, err)
	defer apiResp.Body.Close()
	require.Equal(t, http.StatusOK, apiResp.StatusCode)

	// 2: OAuth2 flow still works alongside it.
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/oauth/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)
	tokResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer tokResp.Body.Close()
	require.Equal(t, http.StatusOK, tokResp.StatusCode)
}

// TestOAuth2_Discovery_NotMountedWithoutProvider ensures the routes
// are absent when auth mode is something other than oauth2 — a
// pagefault deployment with bearer auth should not advertise a
// /oauth/token endpoint at all.
func TestOAuth2_Discovery_NotMountedWithoutProvider(t *testing.T) {
	ts, _ := newTestServer(t, "none", "")

	resp, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp2, err := http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(""))
	require.NoError(t, err)
	_ = resp2.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp2.StatusCode)
}

// TestExtractClientCredentials_URLEncodedBasic covers the RFC 6749
// §2.3.1 requirement that Basic auth values are form-urlencoded
// before base64 — matters when a secret happens to contain a colon
// or a plus sign.
func TestExtractClientCredentials_URLEncodedBasic(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	// Encode client_id="my id" and client_secret="a:b+c"
	id := url.QueryEscape("my id")
	secret := url.QueryEscape("a:b+c")
	raw := id + ":" + secret
	encoded := base64.StdEncoding.EncodeToString([]byte(raw))
	req.Header.Set("Authorization", "Basic "+encoded)
	require.NoError(t, req.ParseForm())

	gotID, gotSecret, method, ok := extractClientCredentials(req)
	require.True(t, ok)
	assert.Equal(t, "my id", gotID)
	assert.Equal(t, "a:b+c", gotSecret)
	assert.Equal(t, "basic", method)
}
