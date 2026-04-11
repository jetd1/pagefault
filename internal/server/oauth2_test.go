package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
		ID:           clientID,
		Label:        "Claude Desktop",
		SecretHash:   hash,
		Scopes:       []string{"mcp"},
		RedirectURIs: []string{"http://localhost:3000/callback"},
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
	grantStrs := make([]string, len(grants))
	for i, g := range grants {
		grantStrs[i] = g.(string)
	}
	assert.Contains(t, grantStrs, "client_credentials")
	assert.Contains(t, grantStrs, "authorization_code")

	methods, _ := body["token_endpoint_auth_methods_supported"].([]any)
	require.Len(t, methods, 3)

	// Authorization endpoint must be present for auth code flow.
	assert.Contains(t, body, "authorization_endpoint")
	authzEndpoint, ok := body["authorization_endpoint"].(string)
	require.True(t, ok)
	assert.Contains(t, authzEndpoint, "/oauth/authorize")

	// PKCE code challenge methods must include S256.
	challengeMethods, _ := body["code_challenge_methods_supported"].([]any)
	require.NotEmpty(t, challengeMethods)
	assert.Equal(t, "S256", challengeMethods[0].(string))

	// Response types must include "code".
	respTypes, _ := body["response_types_supported"].([]any)
	require.NotEmpty(t, respTypes)
	assert.Equal(t, "code", respTypes[0].(string))
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

// ── Authorization code + PKCE integration tests ──

// newOAuth2TestServerWithPublicClient creates a test server seeded with
// a public client (no secret, redirect URIs only).
func newOAuth2TestServerWithPublicClient(t *testing.T) (ts *httptest.Server, clientID string) {
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

	clientID = "public-client"
	clientsPath := filepath.Join(dir, "oauth-clients.jsonl")
	rec := auth.ClientRecord{
		ID:           clientID,
		Label:        "Public Client",
		RedirectURIs: []string{"http://localhost:3000/callback"},
		Scopes:       []string{"mcp"},
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

	provider, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)

	srv, err := New(cfg, d, provider)
	require.NoError(t, err)

	ts = httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts, clientID
}

func TestOAuth2_Authorize_AutoApprove(t *testing.T) {
	ts, clientID := newOAuth2TestServerWithPublicClient(t)

	verifier := "my-code-verifier"
	challenge := computePKCEChallenge(verifier)

	// GET /oauth/authorize should redirect immediately with code+state.
	u := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=teststate",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"), challenge)

	// Use a client that doesn't follow redirects so we can inspect the 302.
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	location := resp.Header.Get("Location")
	assert.Contains(t, location, "http://localhost:3000/callback?")
	assert.Contains(t, location, "code=pf_ac_")
	assert.Contains(t, location, "state=teststate")
}

func TestOAuth2_Authorize_MissingParams(t *testing.T) {
	ts, clientID := newOAuth2TestServerWithPublicClient(t)

	// Missing state → 400 (no redirect because state is missing).
	u := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=test&code_challenge_method=S256",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"))
	resp, err := http.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Missing code_challenge → error redirect.
	u2 := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&state=s1&code_challenge_method=S256",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"))
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp2, err := client.Get(u2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	// Should redirect with error.
	loc := resp2.Header.Get("Location")
	assert.Contains(t, loc, "error=invalid_request")
	assert.Contains(t, loc, "state=s1")
}

func TestOAuth2_Authorize_InvalidRedirectURI(t *testing.T) {
	ts, clientID := newOAuth2TestServerWithPublicClient(t)

	// redirect_uri not registered → 400 (no redirect, prevents open redirect).
	u := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=test&code_challenge_method=S256&state=s1",
		ts.URL, clientID, url.QueryEscape("http://evil.com/callback"))
	resp, err := http.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestOAuth2_Authorize_UnsupportedResponseType(t *testing.T) {
	ts, clientID := newOAuth2TestServerWithPublicClient(t)

	u := fmt.Sprintf("%s/oauth/authorize?response_type=token&client_id=%s&redirect_uri=%s&code_challenge=test&code_challenge_method=S256&state=s1",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"))
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	assert.Contains(t, loc, "error=unsupported_response_type")
}

func TestOAuth2_Authorize_UnsupportedCodeChallengeMethod(t *testing.T) {
	ts, clientID := newOAuth2TestServerWithPublicClient(t)

	u := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=test&code_challenge_method=plain&state=s1",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"))
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	assert.Contains(t, loc, "error=invalid_request")
}

func TestOAuth2_Token_AuthCodeGrant_PublicClient(t *testing.T) {
	ts, clientID := newOAuth2TestServerWithPublicClient(t)

	// Step 1: GET /oauth/authorize to get a code.
	verifier := "my-code-verifier"
	challenge := computePKCEChallenge(verifier)
	u := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=s1",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"), challenge)

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)

	location := resp.Header.Get("Location")
	locURL, err := url.Parse(location)
	require.NoError(t, err)
	code := locURL.Query().Get("code")
	require.NotEmpty(t, code)
	assert.Equal(t, "s1", locURL.Query().Get("state"))

	// Step 2: POST /oauth/token with grant_type=authorization_code.
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", "http://localhost:3000/callback")
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)

	tokResp, err := http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer tokResp.Body.Close()
	require.Equal(t, http.StatusOK, tokResp.StatusCode)

	var tokenBody map[string]any
	require.NoError(t, json.NewDecoder(tokResp.Body).Decode(&tokenBody))
	assert.Equal(t, "Bearer", tokenBody["token_type"])
	assert.NotEmpty(t, tokenBody["access_token"])
	assert.True(t, strings.HasPrefix(tokenBody["access_token"].(string), "pf_at_"))
}

func TestOAuth2_Token_AuthCodeGrant_PKCEFailure(t *testing.T) {
	ts, clientID := newOAuth2TestServerWithPublicClient(t)

	verifier := "correct-verifier"
	challenge := computePKCEChallenge(verifier)
	u := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=s1",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"), challenge)

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	locURL, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	code := locURL.Query().Get("code")

	// Exchange with wrong code_verifier.
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", "http://localhost:3000/callback")
	form.Set("client_id", clientID)
	form.Set("code_verifier", "wrong-verifier")

	tokResp, err := http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer tokResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, tokResp.StatusCode)

	var errBody map[string]any
	require.NoError(t, json.NewDecoder(tokResp.Body).Decode(&errBody))
	assert.Equal(t, "invalid_grant", errBody["error"])
}

func TestOAuth2_Token_AuthCodeGrant_ConsumedCode(t *testing.T) {
	ts, clientID := newOAuth2TestServerWithPublicClient(t)

	verifier := "my-verifier"
	challenge := computePKCEChallenge(verifier)
	u := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=s1",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"), challenge)

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	locURL, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	code := locURL.Query().Get("code")

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", "http://localhost:3000/callback")
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)

	// First exchange succeeds.
	tokResp, err := http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	_ = tokResp.Body.Close()
	require.Equal(t, http.StatusOK, tokResp.StatusCode)

	// Second exchange fails.
	tokResp2, err := http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer tokResp2.Body.Close()
	assert.Equal(t, http.StatusBadRequest, tokResp2.StatusCode)
}

func TestOAuth2_EndToEnd_AuthCodeFlow(t *testing.T) {
	ts, clientID := newOAuth2TestServerWithPublicClient(t)

	// Full flow: authorize → token → API call.
	verifier := "end-to-end-verifier"
	challenge := computePKCEChallenge(verifier)
	u := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=e2e",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"), challenge)

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	locURL, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	code := locURL.Query().Get("code")

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", "http://localhost:3000/callback")
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)

	tokResp, err := http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer tokResp.Body.Close()
	require.Equal(t, http.StatusOK, tokResp.StatusCode)

	var tokenBody map[string]any
	require.NoError(t, json.NewDecoder(tokResp.Body).Decode(&tokenBody))
	accessToken := tokenBody["access_token"].(string)

	// Use the access token on a protected API route.
	apiReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/pf_maps", strings.NewReader("{}"))
	require.NoError(t, err)
	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("Authorization", "Bearer "+accessToken)
	apiResp, err := http.DefaultClient.Do(apiReq)
	require.NoError(t, err)
	defer apiResp.Body.Close()
	assert.Equal(t, http.StatusOK, apiResp.StatusCode)
}

func TestOAuth2_Token_AuthCodeGrant_ConfidentialClient(t *testing.T) {
	ts, clientID, clientSecret := newOAuth2TestServer(t, "")

	verifier := "confidential-verifier"
	challenge := computePKCEChallenge(verifier)
	u := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=s1",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"), challenge)

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	locURL, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	code := locURL.Query().Get("code")

	// Confidential client: use Basic auth + code_verifier.
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", "http://localhost:3000/callback")
	form.Set("code_verifier", verifier)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/oauth/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)

	tokResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer tokResp.Body.Close()
	require.Equal(t, http.StatusOK, tokResp.StatusCode)

	var tokenBody map[string]any
	require.NoError(t, json.NewDecoder(tokResp.Body).Decode(&tokenBody))
	assert.NotEmpty(t, tokenBody["access_token"])
}

// ── 0.8.1 regression tests: open redirect + consent hardening ──

// newOAuth2TestServerConsent spins up a public-client server with the
// given auto_approve flag. Used by the consent-page regression tests
// that exercise the auto_approve=false branch.
func newOAuth2TestServerConsent(t *testing.T, autoApprove bool) (ts *httptest.Server, clientID string) {
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

	clientID = "public-client"
	clientsPath := filepath.Join(dir, "oauth-clients.jsonl")
	rec := auth.ClientRecord{
		ID:           clientID,
		Label:        "Public Client",
		RedirectURIs: []string{"http://localhost:3000/callback"},
		Scopes:       []string{"mcp"},
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
				AutoApprove:           &autoApprove,
			},
		},
	}

	provider, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)

	srv, err := New(cfg, d, provider)
	require.NoError(t, err)

	ts = httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts, clientID
}

// TestOAuth2_Authorize_NoOpenRedirect_UnregisteredURI verifies that
// an invalid response_type with an attacker-controlled redirect_uri
// does NOT bounce the browser through the unregistered URL. Per RFC
// 6749 §4.1.2.1 the authorization server must validate client_id and
// redirect_uri before allowing any other error to trigger a redirect.
//
// Prior to 0.8.1 the response_type check fired first and authorizeError
// 302'd to the attacker URL — a textbook open redirect on a publicly
// advertised endpoint.
func TestOAuth2_Authorize_NoOpenRedirect_UnregisteredURI(t *testing.T) {
	ts, clientID := newOAuth2TestServerWithPublicClient(t)

	evil := "https://evil.example.com/steal"
	u := fmt.Sprintf("%s/oauth/authorize?response_type=bogus&client_id=%s&redirect_uri=%s&code_challenge=x&code_challenge_method=S256&state=attackerstate",
		ts.URL, clientID, url.QueryEscape(evil))

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Must NOT be a redirect to the unregistered URI.
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"unregistered redirect_uri must yield 400, not a 302 open redirect")
	assert.NotContains(t, resp.Header.Get("Location"), "evil.example.com")
}

// TestOAuth2_Authorize_NoOpenRedirect_MissingClientID verifies that
// an attacker cannot bounce the browser by omitting client_id and
// supplying an evil redirect_uri — there is no client to validate the
// URI against, so the handler must refuse rather than redirect.
func TestOAuth2_Authorize_NoOpenRedirect_MissingClientID(t *testing.T) {
	ts, _ := newOAuth2TestServerWithPublicClient(t)

	evil := "https://evil.example.com/steal"
	u := fmt.Sprintf("%s/oauth/authorize?response_type=bogus&redirect_uri=%s&code_challenge=x&code_challenge_method=S256&state=s",
		ts.URL, url.QueryEscape(evil))

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.NotContains(t, resp.Header.Get("Location"), "evil.example.com")
}

// TestOAuth2_Authorize_ConsentPage_ParamInjectionBypass is the
// regression test for the consent-form action injection:
//
//	GET /oauth/authorize?...&action=allow (auto_approve=false)
//	→ render consent page with hidden <input name="action" value="allow">
//	→ user clicks Deny, browser POSTs with action=allow (hidden) + action=deny (button)
//	→ pre-fix r.PostForm.Get("action") returned "allow" (first value)
//	→ post-fix: whitelist drops the injected param AND we take the LAST
//	   action value from the POST body, so the button wins.
//
// This test drives the POST path directly with both values present
// (mirroring what the browser would send if the hidden field had been
// injected) and asserts that the flow returns access_denied.
func TestOAuth2_Authorize_ConsentPage_ParamInjectionBypass(t *testing.T) {
	ts, clientID := newOAuth2TestServerConsent(t, false)

	verifier := "inject-verifier"
	challenge := computePKCEChallenge(verifier)

	// Simulate a browser POST where an attacker-injected hidden field
	// with action=allow precedes the user-clicked action=deny button.
	form := url.Values{}
	form.Set("response_type", "code")
	form.Set("client_id", clientID)
	form.Set("redirect_uri", "http://localhost:3000/callback")
	form.Set("code_challenge", challenge)
	form.Set("code_challenge_method", "S256")
	form.Set("state", "s1")
	// url.Values.Set replaces; use Add to get two "action" entries.
	form.Add("action", "allow") // attacker-injected hidden field (first)
	form.Add("action", "deny")  // user-clicked button (last)

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Post(ts.URL+"/oauth/authorize", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()

	// Fix behaviour: the last "action" wins → deny → access_denied.
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	loc := resp.Header.Get("Location")
	assert.Contains(t, loc, "error=access_denied",
		"last action value must decide the outcome; got Location=%q", loc)
	assert.NotContains(t, loc, "code=pf_ac_",
		"denied consent must not emit an authorization code")
}

// TestOAuth2_Authorize_ConsentPage_WhitelistDropsInjectedAction covers
// the defence-in-depth half of the fix: when the consent page is
// rendered, the server must NOT echo an attacker-supplied `action`
// query parameter back into a hidden form field. Only the OAuth
// whitelist should make it into the HTML.
func TestOAuth2_Authorize_ConsentPage_WhitelistDropsInjectedAction(t *testing.T) {
	ts, clientID := newOAuth2TestServerConsent(t, false)

	verifier := "whitelist-verifier"
	challenge := computePKCEChallenge(verifier)
	u := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=s1&action=allow&foo=bar",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"), challenge)

	resp, err := http.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	// OAuth params must render as hidden fields.
	assert.Contains(t, html, `<input type="hidden" name="client_id"`)
	assert.Contains(t, html, `<input type="hidden" name="state"`)
	assert.Contains(t, html, `<input type="hidden" name="code_challenge"`)
	// Non-whitelist params must NOT render as hidden fields. The
	// submit button legitimately uses name="action" on the two <button>
	// elements — we specifically check that nothing injects a hidden
	// <input> with name="action".
	assert.NotContains(t, html, `<input type="hidden" name="action"`,
		"injected action must not survive into the consent form as a hidden field")
	assert.NotContains(t, html, `name="foo"`,
		"unknown params must not be echoed into the consent form")

	// CSP must pin frame-ancestors to none so the consent page cannot
	// be clickjacked even when auto_approve is flipped off.
	csp := resp.Header.Get("Content-Security-Policy")
	assert.Contains(t, csp, "frame-ancestors 'none'")
}

// TestOAuth2_Authorize_ConsentPage_DefaultDeny verifies that when
// auto_approve is false and the user submits a POST without any
// action value (or with an unrecognised one), the flow is treated as
// a deny rather than silently falling through to issue a code.
func TestOAuth2_Authorize_ConsentPage_DefaultDeny(t *testing.T) {
	ts, clientID := newOAuth2TestServerConsent(t, false)

	verifier := "default-deny-verifier"
	challenge := computePKCEChallenge(verifier)

	form := url.Values{}
	form.Set("response_type", "code")
	form.Set("client_id", clientID)
	form.Set("redirect_uri", "http://localhost:3000/callback")
	form.Set("code_challenge", challenge)
	form.Set("code_challenge_method", "S256")
	form.Set("state", "s1")
	// No action field at all.

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Post(ts.URL+"/oauth/authorize", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	loc := resp.Header.Get("Location")
	assert.Contains(t, loc, "error=access_denied")
	assert.NotContains(t, loc, "code=pf_ac_")
}

// TestOAuth2_Authorize_ConsentPage_Allow verifies the positive branch:
// an explicit action=allow POST issues an authorization code.
// TestComputeExpiresIn pins the clamp behaviour for the OAuth2
// token endpoint's `expires_in` field. The field must never be
// zero or negative — RFC 6749 §5.1 requires a positive integer —
// so very short TTLs or a latency spike between issuance and
// response still report at least 1.
func TestComputeExpiresIn(t *testing.T) {
	ref := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		exp     time.Time
		now     time.Time
		want    int
		wantMin int
	}{
		{
			name: "typical one-hour token",
			exp:  ref.Add(3600 * time.Second),
			now:  ref,
			want: 3600,
		},
		{
			name: "sub-second remaining rounds up to 1",
			exp:  ref.Add(250 * time.Millisecond),
			now:  ref,
			want: 1,
		},
		{
			name: "exactly expired → clamped to 1",
			exp:  ref,
			now:  ref,
			want: 1,
		},
		{
			name: "already expired → clamped to 1",
			exp:  ref.Add(-5 * time.Second),
			now:  ref,
			want: 1,
		},
		{
			name: "1s TTL after 1ms of latency still reports 1",
			exp:  ref.Add(1 * time.Second),
			now:  ref.Add(1 * time.Millisecond),
			want: 1,
		},
		{
			name: "fractional second above 1s rounds up",
			exp:  ref.Add(1500 * time.Millisecond),
			now:  ref,
			want: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeExpiresIn(tc.exp, tc.now)
			assert.Equal(t, tc.want, got)
			assert.GreaterOrEqual(t, got, 1, "RFC 6749 §5.1: expires_in must be positive")
		})
	}
}

func TestOAuth2_Authorize_ConsentPage_Allow(t *testing.T) {
	ts, clientID := newOAuth2TestServerConsent(t, false)

	verifier := "allow-verifier"
	challenge := computePKCEChallenge(verifier)

	form := url.Values{}
	form.Set("response_type", "code")
	form.Set("client_id", clientID)
	form.Set("redirect_uri", "http://localhost:3000/callback")
	form.Set("code_challenge", challenge)
	form.Set("code_challenge_method", "S256")
	form.Set("state", "s1")
	form.Set("action", "allow")

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Post(ts.URL+"/oauth/authorize", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	loc := resp.Header.Get("Location")
	assert.Contains(t, loc, "code=pf_ac_")
	assert.Contains(t, loc, "state=s1")
}

// newOAuth2TestServerWithCORS is like newOAuth2TestServer but enables CORS
// with the given allowed origins. Used for preflight integration tests.
func newOAuth2TestServerWithCORS(t *testing.T, allowedOrigins []string) (ts *httptest.Server, clientID, clientSecret string) {
	t.Helper()

	dir := t.TempDir()
	hello := filepath.Join(dir, "hello.md")
	require.NoError(t, os.WriteFile(hello, []byte("# hello\n"), 0o600))

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

	clientID = "test-client"
	clientSecret = "pf_cs_corstest"
	clientsPath := filepath.Join(dir, "oauth-clients.jsonl")
	hash, err := auth.HashClientSecret(clientSecret)
	require.NoError(t, err)
	rec := auth.ClientRecord{
		ID:           clientID,
		Label:        "CORS Test",
		SecretHash:   hash,
		Scopes:       []string{"mcp"},
		RedirectURIs: []string{"http://localhost:3000/callback"},
	}
	f, err := os.Create(clientsPath)
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(f).Encode(&rec))
	require.NoError(t, f.Close())

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1",
			Port: 0,
			CORS: config.CORSConfig{
				Enabled:          true,
				AllowedOrigins:   allowedOrigins,
				AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
				AllowedHeaders:   []string{"Content-Type", "Authorization"},
				MaxAge:           600,
				AllowCredentials: true,
			},
		},
		Auth: config.AuthConfig{
			Mode: "oauth2",
			OAuth2: config.OAuth2Config{
				ClientsFile:           clientsPath,
				AccessTokenTTLSeconds: 3600,
				DefaultScopes:         []string{"mcp"},
			},
		},
	}

	provider, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)

	srv, err := New(cfg, d, provider)
	require.NoError(t, err)

	ts = httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts, clientID, clientSecret
}

// TestPreflight_MCP_AllowedOrigin verifies that an OPTIONS preflight on /mcp
// from an allowlisted origin returns 204 with CORS headers, without requiring
// authentication.
func TestPreflight_MCP_AllowedOrigin(t *testing.T) {
	ts, _, _ := newOAuth2TestServerWithCORS(t, []string{"https://claude.ai"})

	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/mcp", nil)
	req.Header.Set("Origin", "https://claude.ai")
	req.Header.Set("Access-Control-Request-Method", "POST")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode, "preflight must be 204")
	assert.Equal(t, "https://claude.ai", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Headers"), "Authorization")
}

// TestPreflight_SSE_AllowedOrigin verifies the same for the legacy /sse endpoint.
func TestPreflight_SSE_AllowedOrigin(t *testing.T) {
	ts, _, _ := newOAuth2TestServerWithCORS(t, []string{"https://claude.ai"})

	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/sse", nil)
	req.Header.Set("Origin", "https://claude.ai")
	req.Header.Set("Access-Control-Request-Method", "GET")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode, "preflight must be 204")
	assert.Equal(t, "https://claude.ai", resp.Header.Get("Access-Control-Allow-Origin"))
}

// TestPreflight_MCP_DisallowedOrigin verifies that a preflight from an
// origin NOT in the allowlist still gets 204 (not 401), but no ACAO header.
func TestPreflight_MCP_DisallowedOrigin(t *testing.T) {
	ts, _, _ := newOAuth2TestServerWithCORS(t, []string{"https://claude.ai"})

	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/mcp", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "POST")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode,
		"disallowed preflight still gets 204, browser rejects due to missing ACAO")
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"),
		"no ACAO header for disallowed origin")
}

// ─────────────────── DCR integration tests ───────────────────

// newOAuth2TestServerWithDCR spins up a full pagefault Server with DCR
// enabled. It seeds a clients file and returns the test server.
func newOAuth2TestServerWithDCR(t *testing.T, dcrBearerToken string) *httptest.Server {
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

	// Empty clients file — DCR will populate it.
	clientsPath := filepath.Join(dir, "oauth-clients.jsonl")
	require.NoError(t, os.WriteFile(clientsPath, []byte{}, 0o600))

	dcrEnabled := true
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 0},
		Auth: config.AuthConfig{
			Mode: "oauth2",
			OAuth2: config.OAuth2Config{
				ClientsFile:           clientsPath,
				AccessTokenTTLSeconds: 3600,
				DefaultScopes:         []string{"mcp"},
				DCREnabled:            &dcrEnabled,
				DCRBearerToken:        dcrBearerToken,
			},
		},
	}

	provider, err := auth.NewProvider(cfg.Auth)
	require.NoError(t, err)

	srv, err := New(cfg, d, provider)
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestOAuth2_DCR_HappyPath(t *testing.T) {
	ts := newOAuth2TestServerWithDCR(t, "")

	body := `{
		"redirect_uris": ["http://localhost:3000/callback"],
		"grant_types": ["authorization_code", "refresh_token"],
		"response_types": ["code"],
		"client_name": "Claude Desktop",
		"token_endpoint_auth_method": "none"
	}`

	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	clientID, ok := result["client_id"].(string)
	assert.True(t, ok, "client_id must be a string")
	assert.True(t, strings.HasPrefix(clientID, "pf_dcr_"), "DCR client_id should have pf_dcr_ prefix")
	assert.Nil(t, result["client_secret"], "public client should not get client_secret")
	assert.Equal(t, "none", result["token_endpoint_auth_method"])
	assert.Equal(t, "Claude Desktop", result["client_name"])

	redirectURIs, _ := result["redirect_uris"].([]any)
	assert.NotEmpty(t, redirectURIs)

	grantTypes, _ := result["grant_types"].([]any)
	assert.Contains(t, grantTypes, "authorization_code")

	_, hasIssuedAt := result["client_id_issued_at"]
	assert.True(t, hasIssuedAt, "client_id_issued_at must be present")
}

func TestOAuth2_DCR_MissingRedirectURIs(t *testing.T) {
	ts := newOAuth2TestServerWithDCR(t, "")

	body := `{"grant_types": ["authorization_code"]}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "invalid_redirect_uri", result["error"])
}

func TestOAuth2_DCR_InvalidRedirectURI(t *testing.T) {
	ts := newOAuth2TestServerWithDCR(t, "")

	body := `{"redirect_uris": ["http://evil.com/callback"]}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "invalid_redirect_uri", result["error"])
}

func TestOAuth2_DCR_UnsupportedGrantType(t *testing.T) {
	ts := newOAuth2TestServerWithDCR(t, "")

	body := `{"redirect_uris": ["http://localhost:3000/callback"], "grant_types": ["implicit"]}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "invalid_client_metadata", result["error"])
}

func TestOAuth2_DCR_RefreshTokenAccepted(t *testing.T) {
	ts := newOAuth2TestServerWithDCR(t, "")

	body := `{"redirect_uris": ["http://localhost:3000/callback"], "grant_types": ["authorization_code", "refresh_token"]}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestOAuth2_DCR_Disabled(t *testing.T) {
	// Use the standard test server (no DCR enabled).
	ts, _, _ := newOAuth2TestServer(t, "")

	body := `{"redirect_uris": ["http://localhost:3000/callback"]}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestOAuth2_DCR_BearerTokenGate(t *testing.T) {
	ts := newOAuth2TestServerWithDCR(t, "gate-secret")

	regBody := `{"redirect_uris": ["http://localhost:3000/callback"]}`

	t.Run("missing token", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(regBody))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("wrong token", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/register", strings.NewReader(regBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer wrong-token")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("correct token", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/register", strings.NewReader(regBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer gate-secret")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusCreated, resp.StatusCode)
	})
}

func TestOAuth2_DCR_DiscoveryAdvertisesEndpoint(t *testing.T) {
	t.Run("DCR enabled", func(t *testing.T) {
		ts := newOAuth2TestServerWithDCR(t, "")
		resp, err := http.Get(ts.URL + "/.well-known/oauth-authorization-server")
		require.NoError(t, err)
		defer resp.Body.Close()

		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		_, hasEndpoint := body["registration_endpoint"]
		assert.True(t, hasEndpoint, "registration_endpoint must be present when DCR enabled")
	})

	t.Run("DCR disabled", func(t *testing.T) {
		ts, _, _ := newOAuth2TestServer(t, "")
		resp, err := http.Get(ts.URL + "/.well-known/oauth-authorization-server")
		require.NoError(t, err)
		defer resp.Body.Close()

		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		_, hasEndpoint := body["registration_endpoint"]
		assert.False(t, hasEndpoint, "registration_endpoint must be absent when DCR disabled")
	})
}

func TestOAuth2_DCR_EndToEnd(t *testing.T) {
	ts := newOAuth2TestServerWithDCR(t, "")

	// Step 1: Register a client via DCR.
	regBody := `{
		"redirect_uris": ["http://localhost:3000/callback"],
		"grant_types": ["authorization_code"],
		"response_types": ["code"],
		"client_name": "Test DCR Client",
		"token_endpoint_auth_method": "none"
	}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(regBody))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var regResult map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&regResult))
	clientID := regResult["client_id"].(string)
	require.NotEmpty(t, clientID)

	// Step 2: Hit /oauth/authorize to get an authorization code.
	// Use PKCE: code_verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	//           code_challenge = Base64URL(SHA256(code_verifier))
	codeVerifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	h := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

	authURL := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=test-state",
		ts.URL, clientID, url.QueryEscape("http://localhost:3000/callback"), codeChallenge)

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err = client.Get(authURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)

	location := resp.Header.Get("Location")
	require.Contains(t, location, "code=")

	// Extract the authorization code.
	locURL, err := url.Parse(location)
	require.NoError(t, err)
	code := locURL.Query().Get("code")
	require.NotEmpty(t, code)

	// Step 3: Exchange the code for an access token.
	tokenBody := fmt.Sprintf(
		"grant_type=authorization_code&code=%s&redirect_uri=%s&client_id=%s&code_verifier=%s",
		code, url.QueryEscape("http://localhost:3000/callback"), clientID, codeVerifier,
	)
	resp, err = http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(tokenBody))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var tokenResult map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tokenResult))
	accessToken, ok := tokenResult["access_token"].(string)
	assert.True(t, ok, "must get access_token")
	assert.NotEmpty(t, accessToken)

	// Step 4: Use the token to call a protected API.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/pf_maps", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestOAuth2_DCR_InvalidJSON(t *testing.T) {
	ts := newOAuth2TestServerWithDCR(t, "")

	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader("{not json}"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "invalid_client_metadata", result["error"])
}
