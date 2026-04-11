package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

// seedClients writes a clients JSONL file with the given records and
// returns the path. The secret is hashed via HashClientSecret — same
// as the CLI path — so the records parse cleanly in NewOAuth2Provider.
func seedClients(t *testing.T, dir string, clients map[string]string) (path string, hashes map[string]string) {
	t.Helper()
	hashes = map[string]string{}
	path = filepath.Join(dir, "clients.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	for id, secret := range clients {
		h, err := HashClientSecret(secret)
		require.NoError(t, err)
		hashes[id] = h
		rec := ClientRecord{
			ID:         id,
			Label:      id + "-label",
			SecretHash: h,
			Scopes:     []string{"mcp"},
		}
		require.NoError(t, json.NewEncoder(f).Encode(&rec))
	}
	return path, hashes
}

// newOAuth2Provider builds an OAuth2Provider from the seeded clients
// file. The helper exists to keep each test short and to pick a
// short TTL for the expiry tests.
func newOAuth2ProviderForTest(t *testing.T, path string, ttlSeconds int) *OAuth2Provider {
	t.Helper()
	cfg := config.AuthConfig{
		Mode: "oauth2",
		OAuth2: config.OAuth2Config{
			ClientsFile:           path,
			AccessTokenTTLSeconds: ttlSeconds,
			DefaultScopes:         []string{"mcp"},
		},
	}
	p, err := NewOAuth2Provider(cfg)
	require.NoError(t, err)
	return p
}

// newOAuth2ProviderWithAuthCodeTTL is like newOAuth2ProviderForTest
// but also sets a custom auth code TTL (for expiry tests).
func newOAuth2ProviderWithAuthCodeTTL(t *testing.T, path string, accessTokenTTL, authCodeTTL int) *OAuth2Provider {
	t.Helper()
	cfg := config.AuthConfig{
		Mode: "oauth2",
		OAuth2: config.OAuth2Config{
			ClientsFile:           path,
			AccessTokenTTLSeconds: accessTokenTTL,
			AuthCodeTTLSeconds:    authCodeTTL,
			DefaultScopes:         []string{"mcp"},
		},
	}
	p, err := NewOAuth2Provider(cfg)
	require.NoError(t, err)
	return p
}

func TestOAuth2Provider_IssueToken_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"claude-desktop": "s3cret",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)

	tok, err := p.IssueToken(context.Background(), "claude-desktop", "s3cret", nil)
	require.NoError(t, err)
	require.NotNil(t, tok)
	assert.Equal(t, "claude-desktop", tok.ClientID)
	assert.NotEmpty(t, tok.AccessToken)
	assert.True(t, time.Until(tok.ExpiresAt) > 3500*time.Second)
	assert.Equal(t, []string{"mcp"}, tok.Scopes)
}

func TestOAuth2Provider_IssueToken_InvalidSecret(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"claude-desktop": "s3cret",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)

	_, err := p.IssueToken(context.Background(), "claude-desktop", "wrong", nil)
	assert.ErrorIs(t, err, ErrInvalidClient)
}

func TestOAuth2Provider_IssueToken_UnknownClient(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"claude-desktop": "s3cret",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)

	_, err := p.IssueToken(context.Background(), "ghost", "s3cret", nil)
	assert.ErrorIs(t, err, ErrInvalidClient)
}

func TestOAuth2Provider_IssueToken_EmptyInputs(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"claude-desktop": "s3cret",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)

	_, err := p.IssueToken(context.Background(), "", "s3cret", nil)
	assert.ErrorIs(t, err, ErrInvalidClient)

	_, err = p.IssueToken(context.Background(), "claude-desktop", "", nil)
	assert.ErrorIs(t, err, ErrInvalidClient)
}

func TestOAuth2Provider_Authenticate_IssuedToken(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"claude-desktop": "s3cret",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)

	issued, err := p.IssueToken(context.Background(), "claude-desktop", "s3cret", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+issued.AccessToken)
	caller, err := p.Authenticate(req)
	require.NoError(t, err)
	assert.Equal(t, "claude-desktop", caller.ID)
	assert.Equal(t, "claude-desktop-label", caller.Label)
	assert.Equal(t, "oauth2", caller.Metadata["auth"])
}

func TestOAuth2Provider_Authenticate_ExpiredTokenIsRemoved(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"claude-desktop": "s3cret",
	})
	// 1s TTL so the token expires almost immediately.
	p := newOAuth2ProviderForTest(t, path, 1)

	issued, err := p.IssueToken(context.Background(), "claude-desktop", "s3cret", nil)
	require.NoError(t, err)

	// Wait just past the TTL so the token is expired when we look it up.
	time.Sleep(1100 * time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+issued.AccessToken)
	_, err = p.Authenticate(req)
	assert.ErrorIs(t, err, model.ErrUnauthenticated)

	// The expired entry should have been swept from the store.
	p.mu.RLock()
	_, stillThere := p.issued[issued.AccessToken]
	p.mu.RUnlock()
	assert.False(t, stillThere, "expired token should be removed from store")
}

func TestOAuth2Provider_Authenticate_UnknownToken(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"claude-desktop": "s3cret",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer pf_at_totallynotreal")
	_, err := p.Authenticate(req)
	assert.ErrorIs(t, err, model.ErrUnauthenticated)
}

func TestOAuth2Provider_Authenticate_MissingHeader(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"claude-desktop": "s3cret",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	_, err := p.Authenticate(req)
	assert.ErrorIs(t, err, model.ErrUnauthenticated)
}

// TestOAuth2Provider_CompoundModeFallback asserts that when the OAuth2
// provider is configured alongside a tokens_file, long-lived bearer
// tokens from the JSONL store still work. This is the compound-mode
// path that lets operators migrate Claude Desktop to OAuth2 without
// breaking Claude Code deployments.
func TestOAuth2Provider_CompoundModeFallback(t *testing.T) {
	dir := t.TempDir()

	// Seed an oauth2 clients file.
	clientsPath, _ := seedClients(t, dir, map[string]string{
		"claude-desktop": "s3cret",
	})

	// Seed a legacy bearer tokens file.
	tokensPath := filepath.Join(dir, "tokens.jsonl")
	legacyToken := "pf_legacy_bearer_abc"
	tokensJSON := `{"id":"legacy","token":"` + legacyToken + `","label":"Legacy"}` + "\n"
	require.NoError(t, os.WriteFile(tokensPath, []byte(tokensJSON), 0o600))

	cfg := config.AuthConfig{
		Mode: "oauth2",
		OAuth2: config.OAuth2Config{
			ClientsFile:           clientsPath,
			AccessTokenTTLSeconds: 3600,
			DefaultScopes:         []string{"mcp"},
		},
		Bearer: config.BearerAuthConfig{TokensFile: tokensPath},
	}
	p, err := NewOAuth2Provider(cfg)
	require.NoError(t, err)
	require.NotNil(t, p.fallback)

	// The legacy bearer token should authenticate through the fallback.
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+legacyToken)
	caller, err := p.Authenticate(req)
	require.NoError(t, err)
	assert.Equal(t, "legacy", caller.ID)

	// An issued OAuth2 token should also work, via the primary path.
	issued, err := p.IssueToken(context.Background(), "claude-desktop", "s3cret", nil)
	require.NoError(t, err)
	req2 := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req2.Header.Set("Authorization", "Bearer "+issued.AccessToken)
	caller2, err := p.Authenticate(req2)
	require.NoError(t, err)
	assert.Equal(t, "claude-desktop", caller2.ID)
}

func TestOAuth2Provider_ScopeIntersection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.jsonl")
	h, err := HashClientSecret("s3cret")
	require.NoError(t, err)
	rec := ClientRecord{
		ID:         "scoped",
		Label:      "Scoped",
		SecretHash: h,
		Scopes:     []string{"mcp", "mcp.read"},
	}
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(f).Encode(&rec))
	require.NoError(t, f.Close())

	p := newOAuth2ProviderForTest(t, path, 3600)

	// No requested scopes → get the client's full allowed set.
	issued, err := p.IssueToken(context.Background(), "scoped", "s3cret", nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"mcp", "mcp.read"}, issued.Scopes)

	// Requested subset → intersection.
	issued2, err := p.IssueToken(context.Background(), "scoped", "s3cret", []string{"mcp"})
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp"}, issued2.Scopes)

	// Requested scope not in allowed list → filtered out.
	issued3, err := p.IssueToken(context.Background(), "scoped", "s3cret", []string{"mcp.admin", "mcp"})
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp"}, issued3.Scopes)
}

func TestOAuth2Provider_ReloadClients(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"first": "s3cret1",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)
	assert.Equal(t, 1, p.ClientCount())

	// Append a second client and reload.
	h, err := HashClientSecret("s3cret2")
	require.NoError(t, err)
	rec := ClientRecord{
		ID:         "second",
		Label:      "Second",
		SecretHash: h,
		Scopes:     []string{"mcp"},
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(f).Encode(&rec))
	require.NoError(t, f.Close())

	require.NoError(t, p.ReloadClients())
	assert.Equal(t, 2, p.ClientCount())

	// And the new client can now get tokens.
	_, err = p.IssueToken(context.Background(), "second", "s3cret2", nil)
	require.NoError(t, err)
}

// TestOAuth2Provider_ReloadClients_SweepsRevokedTokens asserts that
// when a client disappears from the reloaded file, every access
// token still in the in-memory store for that client is purged.
// This is the hook that makes file-based revocation (rewrite JSONL
// → ReloadClients) fully cut off an already-authenticated client in
// a single step, rather than waiting for the access_token TTL.
func TestOAuth2Provider_ReloadClients_SweepsRevokedTokens(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"alice": "a3cret",
		"bob":   "b3cret",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)

	// Issue tokens for both clients.
	aliceTok, err := p.IssueToken(context.Background(), "alice", "a3cret", nil)
	require.NoError(t, err)
	bobTok, err := p.IssueToken(context.Background(), "bob", "b3cret", nil)
	require.NoError(t, err)

	// Rewrite the file without alice (seedClients truncates).
	_, _ = seedClients(t, dir, map[string]string{
		"bob": "b3cret",
	})
	require.NoError(t, p.ReloadClients())
	assert.Equal(t, 1, p.ClientCount())

	// alice's token is swept → Authenticate fails.
	reqA := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	reqA.Header.Set("Authorization", "Bearer "+aliceTok.AccessToken)
	_, err = p.Authenticate(reqA)
	assert.ErrorIs(t, err, model.ErrUnauthenticated)

	// bob's token survives the reload.
	reqB := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	reqB.Header.Set("Authorization", "Bearer "+bobTok.AccessToken)
	caller, err := p.Authenticate(reqB)
	require.NoError(t, err)
	assert.Equal(t, "bob", caller.ID)
}

// TestOAuth2Provider_RevokeClient asserts the in-memory revoke path
// removes the client record and every issued access token for that
// client. This is the method a future admin endpoint (or SIGHUP
// reload handler) would call to force immediate invalidation.
func TestOAuth2Provider_RevokeClient(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"alice": "a3cret",
		"bob":   "b3cret",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)

	// Two tokens for alice, one for bob.
	a1, err := p.IssueToken(context.Background(), "alice", "a3cret", nil)
	require.NoError(t, err)
	a2, err := p.IssueToken(context.Background(), "alice", "a3cret", nil)
	require.NoError(t, err)
	b1, err := p.IssueToken(context.Background(), "bob", "b3cret", nil)
	require.NoError(t, err)

	n := p.RevokeClient("alice")
	assert.Equal(t, 2, n)

	// alice's tokens are both gone.
	for _, tok := range []string{a1.AccessToken, a2.AccessToken} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		_, err := p.Authenticate(req)
		assert.ErrorIs(t, err, model.ErrUnauthenticated)
	}
	// bob's token still works.
	reqB := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	reqB.Header.Set("Authorization", "Bearer "+b1.AccessToken)
	_, err = p.Authenticate(reqB)
	require.NoError(t, err)

	// alice can no longer get a new token either.
	_, err = p.IssueToken(context.Background(), "alice", "a3cret", nil)
	assert.ErrorIs(t, err, ErrInvalidClient)

	// Revoking an unknown client returns 0, no panic, no error.
	assert.Equal(t, 0, p.RevokeClient("ghost"))
}

func TestNewOAuth2Provider_RejectsMissingClientsFile(t *testing.T) {
	cfg := config.AuthConfig{
		Mode:   "oauth2",
		OAuth2: config.OAuth2Config{},
	}
	_, err := NewOAuth2Provider(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clients_file is required")
}

func TestNewOAuth2Provider_RejectsDuplicateClientID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	h, err := HashClientSecret("s3cret")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		require.NoError(t, json.NewEncoder(f).Encode(&ClientRecord{
			ID: "dupe", Label: "Dupe", SecretHash: h, Scopes: []string{"mcp"},
		}))
	}
	require.NoError(t, f.Close())

	cfg := config.AuthConfig{
		Mode:   "oauth2",
		OAuth2: config.OAuth2Config{ClientsFile: path},
	}
	_, err = NewOAuth2Provider(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate client id")
}

func TestParseClientsJSONL_SkipsCommentsAndBlanks(t *testing.T) {
	data := []byte(`# a comment
{"id":"one","label":"One","secret_hash":"x","scopes":["mcp"]}

  # indented comment
{"id":"two","label":"Two","secret_hash":"y","scopes":["mcp"]}
`)
	recs, err := ParseClientsJSONL(data)
	require.NoError(t, err)
	require.Len(t, recs, 2)
	assert.Equal(t, "one", recs[0].ID)
	assert.Equal(t, "two", recs[1].ID)
}

func TestGenerateAccessTokenAndClientSecret_Unique(t *testing.T) {
	a, err := generateAccessToken()
	require.NoError(t, err)
	b, err := generateAccessToken()
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	assert.True(t, len(a) > len("pf_at_")+20)

	s1, err := GenerateClientSecret()
	require.NoError(t, err)
	s2, err := GenerateClientSecret()
	require.NoError(t, err)
	assert.NotEqual(t, s1, s2)
	assert.True(t, len(s1) > len("pf_cs_")+20)
}

// ── Authorization code + PKCE tests ──

// seedPublicClient writes a clients JSONL file with a public client
// (no secret, redirect_uris only) and returns the path.
func seedPublicClient(t *testing.T, dir, id string, redirectURIs []string) string {
	t.Helper()
	path := filepath.Join(dir, "clients.jsonl")
	rec := ClientRecord{
		ID:           id,
		Label:        id + "-label",
		RedirectURIs: redirectURIs,
		Scopes:       []string{"mcp"},
	}
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(f).Encode(&rec))
	require.NoError(t, f.Close())
	return path
}

// seedMixedClients writes a clients JSONL with one confidential and
// one public client.
func seedMixedClients(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "clients.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	// Confidential client.
	h, err := HashClientSecret("s3cret")
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(f).Encode(&ClientRecord{
		ID: "confidential", Label: "Confidential",
		SecretHash: h, Scopes: []string{"mcp"},
	}))
	// Public client.
	require.NoError(t, json.NewEncoder(f).Encode(&ClientRecord{
		ID: "public", Label: "Public",
		RedirectURIs: []string{"http://localhost:3000/callback"},
		Scopes:       []string{"mcp"},
	}))
	require.NoError(t, f.Close())
	return path
}

// pkceChallenge computes BASE64URL(SHA256(verifier)) — the S256
// code_challenge method from RFC 7636.
func pkceChallenge(t *testing.T, verifier string) string {
	t.Helper()
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func TestOAuth2Provider_IssueAuthorizationCode_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := seedMixedClients(t, dir)
	p := newOAuth2ProviderForTest(t, path, 3600)

	challenge := pkceChallenge(t, "my-code-verifier")
	ac, err := p.IssueAuthorizationCode("public", "http://localhost:3000/callback", nil, "test-state", challenge, "S256")
	require.NoError(t, err)
	assert.NotEmpty(t, ac.Code)
	assert.True(t, strings.HasPrefix(ac.Code, "pf_ac_"))
	assert.Equal(t, "public", ac.ClientID)
	assert.Equal(t, "http://localhost:3000/callback", ac.RedirectURI)
	assert.Equal(t, "test-state", ac.State)
	assert.Equal(t, challenge, ac.CodeChallenge)
	assert.Equal(t, "S256", ac.CodeChallengeMethod)
	assert.False(t, ac.Consumed)
	assert.True(t, time.Until(ac.ExpiresAt) > 50*time.Second)
}

func TestOAuth2Provider_IssueAuthorizationCode_UnknownClient(t *testing.T) {
	dir := t.TempDir()
	path := seedMixedClients(t, dir)
	p := newOAuth2ProviderForTest(t, path, 3600)

	_, err := p.IssueAuthorizationCode("ghost", "http://localhost:3000/callback", nil, "state", "challenge", "S256")
	assert.ErrorIs(t, err, ErrInvalidClient)
}

func TestOAuth2Provider_IssueAuthorizationCode_InvalidRedirectURI(t *testing.T) {
	dir := t.TempDir()
	path := seedMixedClients(t, dir)
	p := newOAuth2ProviderForTest(t, path, 3600)

	_, err := p.IssueAuthorizationCode("public", "http://evil.com/callback", nil, "state", "challenge", "S256")
	assert.ErrorIs(t, err, model.ErrInvalidRequest)
}

func TestOAuth2Provider_IssueAuthorizationCode_NoRegisteredURIs(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedClients(t, dir, map[string]string{
		"confidential": "s3cret",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)

	// Confidential client has no redirect_uris — auth code flow not
	// allowed for this client. Exercise both the non-empty and the
	// empty redirectURI arg to pin the 0.8.1 library-level guard
	// (pre-fix, an empty redirectURI would fall through the
	// validation without error).
	_, err := p.IssueAuthorizationCode("confidential", "http://localhost:3000/callback", nil, "state", "challenge", "S256")
	assert.ErrorIs(t, err, model.ErrInvalidRequest)

	_, err = p.IssueAuthorizationCode("confidential", "", nil, "state", "challenge", "S256")
	assert.ErrorIs(t, err, model.ErrInvalidRequest,
		"library callers must also be rejected when both the client has no registered URIs AND the supplied redirect_uri is empty")
}

func TestOAuth2Provider_ExchangeAuthorizationCode_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := seedMixedClients(t, dir)
	p := newOAuth2ProviderForTest(t, path, 3600)

	verifier := "my-code-verifier"
	challenge := pkceChallenge(t, verifier)
	ac, err := p.IssueAuthorizationCode("public", "http://localhost:3000/callback", nil, "state", challenge, "S256")
	require.NoError(t, err)

	tok, err := p.ExchangeAuthorizationCode(ac.Code, "http://localhost:3000/callback", "public", verifier)
	require.NoError(t, err)
	assert.NotEmpty(t, tok.AccessToken)
	assert.True(t, strings.HasPrefix(tok.AccessToken, "pf_at_"))
	assert.Equal(t, "public", tok.ClientID)
	assert.Equal(t, []string{"mcp"}, tok.Scopes)
}

func TestOAuth2Provider_ExchangeAuthorizationCode_ExpiredCode(t *testing.T) {
	dir := t.TempDir()
	path := seedMixedClients(t, dir)
	// 1-second auth code TTL so the code expires fast.
	p := newOAuth2ProviderWithAuthCodeTTL(t, path, 3600, 1)

	verifier := "my-code-verifier"
	challenge := pkceChallenge(t, verifier)
	ac, err := p.IssueAuthorizationCode("public", "http://localhost:3000/callback", nil, "state", challenge, "S256")
	require.NoError(t, err)

	time.Sleep(1100 * time.Millisecond)

	_, err = p.ExchangeAuthorizationCode(ac.Code, "http://localhost:3000/callback", "public", verifier)
	assert.ErrorIs(t, err, ErrInvalidGrant)
}

func TestOAuth2Provider_ExchangeAuthorizationCode_ConsumedCode(t *testing.T) {
	dir := t.TempDir()
	path := seedMixedClients(t, dir)
	p := newOAuth2ProviderForTest(t, path, 3600)

	verifier := "my-code-verifier"
	challenge := pkceChallenge(t, verifier)
	ac, err := p.IssueAuthorizationCode("public", "http://localhost:3000/callback", nil, "state", challenge, "S256")
	require.NoError(t, err)

	// First exchange succeeds.
	_, err = p.ExchangeAuthorizationCode(ac.Code, "http://localhost:3000/callback", "public", verifier)
	require.NoError(t, err)

	// Second exchange fails — code already consumed.
	_, err = p.ExchangeAuthorizationCode(ac.Code, "http://localhost:3000/callback", "public", verifier)
	assert.ErrorIs(t, err, ErrInvalidGrant)
}

func TestOAuth2Provider_ExchangeAuthorizationCode_WrongRedirectURI(t *testing.T) {
	dir := t.TempDir()
	path := seedMixedClients(t, dir)
	p := newOAuth2ProviderForTest(t, path, 3600)

	verifier := "my-code-verifier"
	challenge := pkceChallenge(t, verifier)
	ac, err := p.IssueAuthorizationCode("public", "http://localhost:3000/callback", nil, "state", challenge, "S256")
	require.NoError(t, err)

	_, err = p.ExchangeAuthorizationCode(ac.Code, "http://evil.com/callback", "public", verifier)
	assert.ErrorIs(t, err, ErrInvalidGrant)
}

func TestOAuth2Provider_ExchangeAuthorizationCode_WrongClientID(t *testing.T) {
	dir := t.TempDir()
	path := seedMixedClients(t, dir)
	p := newOAuth2ProviderForTest(t, path, 3600)

	verifier := "my-code-verifier"
	challenge := pkceChallenge(t, verifier)
	ac, err := p.IssueAuthorizationCode("public", "http://localhost:3000/callback", nil, "state", challenge, "S256")
	require.NoError(t, err)

	_, err = p.ExchangeAuthorizationCode(ac.Code, "http://localhost:3000/callback", "wrong-client", verifier)
	assert.ErrorIs(t, err, ErrInvalidGrant)
}

func TestOAuth2Provider_ExchangeAuthorizationCode_PKCEVerification(t *testing.T) {
	dir := t.TempDir()
	path := seedMixedClients(t, dir)
	p := newOAuth2ProviderForTest(t, path, 3600)

	verifier := "correct-code-verifier"
	challenge := pkceChallenge(t, verifier)
	ac, err := p.IssueAuthorizationCode("public", "http://localhost:3000/callback", nil, "state", challenge, "S256")
	require.NoError(t, err)

	// Wrong verifier → PKCE failure → invalid_grant.
	_, err = p.ExchangeAuthorizationCode(ac.Code, "http://localhost:3000/callback", "public", "wrong-verifier")
	assert.ErrorIs(t, err, ErrInvalidGrant)
}

func TestOAuth2Provider_ExchangeAuthorizationCode_UnknownCode(t *testing.T) {
	dir := t.TempDir()
	path := seedMixedClients(t, dir)
	p := newOAuth2ProviderForTest(t, path, 3600)

	_, err := p.ExchangeAuthorizationCode("pf_ac_nonexistent", "http://localhost:3000/callback", "public", "verifier")
	assert.ErrorIs(t, err, ErrInvalidGrant)
}

func TestVerifyCodeChallenge(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	// Expected challenge from RFC 7636 Appendix B.
	challenge := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	assert.True(t, verifyCodeChallenge(verifier, challenge))
	assert.False(t, verifyCodeChallenge("wrong", challenge))
	assert.False(t, verifyCodeChallenge(verifier, "wrong"))
}

func TestOAuth2Provider_PublicClient_NoSecret(t *testing.T) {
	dir := t.TempDir()
	path := seedPublicClient(t, dir, "claude-desktop", []string{
		"http://localhost:3000/callback",
		"http://127.0.0.1:3000/callback",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)
	assert.Equal(t, 1, p.ClientCount())

	// Public client can use auth code flow.
	verifier := "my-verifier"
	challenge := pkceChallenge(t, verifier)
	ac, err := p.IssueAuthorizationCode("claude-desktop", "http://localhost:3000/callback", nil, "state", challenge, "S256")
	require.NoError(t, err)

	tok, err := p.ExchangeAuthorizationCode(ac.Code, "http://localhost:3000/callback", "claude-desktop", verifier)
	require.NoError(t, err)
	assert.Equal(t, "claude-desktop", tok.ClientID)

	// Public client cannot use client_credentials (no secret).
	_, err = p.IssueToken(context.Background(), "claude-desktop", "", nil)
	assert.ErrorIs(t, err, ErrInvalidClient)
}

func TestOAuth2Provider_PublicClient_MultipleRedirectURIs(t *testing.T) {
	dir := t.TempDir()
	path := seedPublicClient(t, dir, "multi", []string{
		"http://localhost:3000/callback",
		"http://127.0.0.1:3000/callback",
	})
	p := newOAuth2ProviderForTest(t, path, 3600)

	verifier := "v"
	challenge := pkceChallenge(t, verifier)

	// Both redirect URIs work.
	ac1, err := p.IssueAuthorizationCode("multi", "http://localhost:3000/callback", nil, "s1", challenge, "S256")
	require.NoError(t, err)
	assert.NotNil(t, ac1)

	ac2, err := p.IssueAuthorizationCode("multi", "http://127.0.0.1:3000/callback", nil, "s2", challenge, "S256")
	require.NoError(t, err)
	assert.NotNil(t, ac2)
}

func TestNewOAuth2Provider_RejectsRecordWithNoSecretAndNoURIs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(f).Encode(&ClientRecord{
		ID: "bad", Label: "Bad", Scopes: []string{"mcp"},
	}))
	require.NoError(t, f.Close())

	cfg := config.AuthConfig{
		Mode:   "oauth2",
		OAuth2: config.OAuth2Config{ClientsFile: path},
	}
	_, err = NewOAuth2Provider(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty secret_hash and no redirect_uris")
}

func TestGenerateAuthCode_Unique(t *testing.T) {
	a, err := generateAuthCode()
	require.NoError(t, err)
	b, err := generateAuthCode()
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	assert.True(t, strings.HasPrefix(a, "pf_ac_"))
}

// ─────────────────── DCR helpers ───────────────────

// newOAuth2ProviderWithDCR builds an OAuth2Provider with DCR enabled.
func newOAuth2ProviderWithDCR(t *testing.T, path string, dcrBearerToken string) *OAuth2Provider {
	t.Helper()
	dcrEnabled := true
	cfg := config.AuthConfig{
		Mode: "oauth2",
		OAuth2: config.OAuth2Config{
			ClientsFile:           path,
			AccessTokenTTLSeconds: 3600,
			DefaultScopes:         []string{"mcp"},
			DCREnabled:            &dcrEnabled,
			DCRBearerToken:        dcrBearerToken,
		},
	}
	p, err := NewOAuth2Provider(cfg)
	require.NoError(t, err)
	return p
}

// seedEmptyClientsFile creates an empty clients JSONL file and returns
// its path. Used by DCR tests that start with no pre-registered clients.
func seedEmptyClientsFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "clients.jsonl")
	// Write an empty file — pagefault accepts empty JSONL.
	require.NoError(t, os.WriteFile(path, []byte{}, 0o600))
	return path
}

// ─────────────────── DCR tests ───────────────────

func TestGenerateClientID_Unique(t *testing.T) {
	a, err := GenerateClientID()
	require.NoError(t, err)
	b, err := GenerateClientID()
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	assert.True(t, strings.HasPrefix(a, "pf_dcr_"))
}

func TestIsLocalhostOrHTTPS(t *testing.T) {
	tests := []struct {
		uri string
		ok  bool
	}{
		{"http://localhost:3000/callback", true},
		{"http://127.0.0.1:8080/cb", true},
		{"http://[::1]:3000/callback", true},
		{"https://example.com/callback", true},
		{"https://pagefault.jetd.one/oauth/callback", true},
		{"http://evil.com/callback", false},
		{"http://192.168.1.1/callback", false},
		{"ftp://localhost/callback", false},
		{"", false},
		{"not-a-url", false},
	}
	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			assert.Equal(t, tt.ok, isLocalhostOrHTTPS(tt.uri))
		})
	}
}

func TestRegisterClient_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	rec, err := p.RegisterClient(DCRRequest{
		RedirectURIs:  []string{"http://localhost:3000/callback"},
		GrantTypes:    []string{"authorization_code"},
		ResponseTypes: []string{"code"},
		ClientName:    "Claude Desktop",
	})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(rec.ID, "pf_dcr_"))
	assert.Equal(t, "Claude Desktop", rec.Label)
	assert.Empty(t, rec.SecretHash, "DCR creates public clients only")
	assert.Equal(t, []string{"http://localhost:3000/callback"}, rec.RedirectURIs)
	assert.Equal(t, []string{"mcp"}, rec.Scopes)

	// Verify in-memory presence.
	got, ok := p.LookupClient(rec.ID)
	assert.True(t, ok)
	assert.Equal(t, rec.ID, got.ID)

	// Verify JSONL file has the record.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), rec.ID)

	// Verify dcr flag in metadata.
	dcrFlag, ok := rec.Metadata["dcr"].(bool)
	assert.True(t, ok && dcrFlag, "metadata should have dcr=true")
}

func TestRegisterClient_MissingRedirectURIs(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	_, err := p.RegisterClient(DCRRequest{
		GrantTypes: []string{"authorization_code"},
	})
	require.Error(t, err)
	var dcrErr *DCRError
	assert.ErrorAs(t, err, &dcrErr)
	assert.Equal(t, "invalid_redirect_uri", dcrErr.Code)
}

func TestRegisterClient_NonLocalhostHTTPRedirectURI(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	_, err := p.RegisterClient(DCRRequest{
		RedirectURIs: []string{"http://evil.com/callback"},
	})
	require.Error(t, err)
	var dcrErr *DCRError
	assert.ErrorAs(t, err, &dcrErr)
	assert.Equal(t, "invalid_redirect_uri", dcrErr.Code)
}

func TestRegisterClient_HTTPSRedirectURI(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	rec, err := p.RegisterClient(DCRRequest{
		RedirectURIs: []string{"https://example.com/callback"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"https://example.com/callback"}, rec.RedirectURIs)
}

func TestRegisterClient_UnsupportedGrantType(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	_, err := p.RegisterClient(DCRRequest{
		RedirectURIs: []string{"http://localhost:3000/callback"},
		GrantTypes:   []string{"implicit"},
	})
	require.Error(t, err)
	var dcrErr *DCRError
	assert.ErrorAs(t, err, &dcrErr)
	assert.Equal(t, "invalid_client_metadata", dcrErr.Code)
}

func TestRegisterClient_RefreshTokenSilentlyAccepted(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	rec, err := p.RegisterClient(DCRRequest{
		RedirectURIs: []string{"http://localhost:3000/callback"},
		GrantTypes:   []string{"authorization_code", "refresh_token"},
	})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(rec.ID, "pf_dcr_"))
}

func TestRegisterClient_InvalidAuthMethod(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	_, err := p.RegisterClient(DCRRequest{
		RedirectURIs:            []string{"http://localhost:3000/callback"},
		TokenEndpointAuthMethod: "client_secret_basic",
	})
	require.Error(t, err)
	var dcrErr *DCRError
	assert.ErrorAs(t, err, &dcrErr)
	assert.Equal(t, "invalid_client_metadata", dcrErr.Code)
}

func TestRegisterClient_AuthMethodNone(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	rec, err := p.RegisterClient(DCRRequest{
		RedirectURIs:            []string{"http://localhost:3000/callback"},
		TokenEndpointAuthMethod: "none",
	})
	require.NoError(t, err)
	assert.Empty(t, rec.SecretHash)
}

func TestRegisterClient_ScopeDefaults(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	rec, err := p.RegisterClient(DCRRequest{
		RedirectURIs: []string{"http://localhost:3000/callback"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp"}, rec.Scopes, "empty scope should fall back to provider default")
}

func TestRegisterClient_CustomScope(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	rec, err := p.RegisterClient(DCRRequest{
		RedirectURIs: []string{"http://localhost:3000/callback"},
		Scope:        "mcp admin",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp", "admin"}, rec.Scopes)
}

func TestRegisterClient_DCRDisabled(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderForTest(t, path, 3600) // no DCR

	_, err := p.RegisterClient(DCRRequest{
		RedirectURIs: []string{"http://localhost:3000/callback"},
	})
	assert.ErrorIs(t, err, ErrDCRDisabled)
}

func TestRegisterClient_PersistSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	rec, err := p.RegisterClient(DCRRequest{
		RedirectURIs: []string{"http://localhost:3000/callback"},
		ClientName:   "Test Client",
	})
	require.NoError(t, err)

	// Reload from the JSONL file.
	require.NoError(t, p.ReloadClients())

	got, ok := p.LookupClient(rec.ID)
	assert.True(t, ok, "DCR-registered client must survive reload")
	assert.Equal(t, "Test Client", got.Label)
}

func TestRegisterClient_Concurrent(t *testing.T) {
	dir := t.TempDir()
	path := seedEmptyClientsFile(t, dir)
	p := newOAuth2ProviderWithDCR(t, path, "")

	errCh := make(chan error, 2)
	var rec1, rec2 *ClientRecord

	go func() {
		r, err := p.RegisterClient(DCRRequest{
			RedirectURIs: []string{"http://localhost:3001/callback"},
			ClientName:   "Client 1",
		})
		rec1 = r
		errCh <- err
	}()
	go func() {
		r, err := p.RegisterClient(DCRRequest{
			RedirectURIs: []string{"http://localhost:3002/callback"},
			ClientName:   "Client 2",
		})
		rec2 = r
		errCh <- err
	}()

	require.NoError(t, <-errCh)
	require.NoError(t, <-errCh)
	assert.NotEqual(t, rec1.ID, rec2.ID, "concurrent registrations must get unique IDs")

	// Both should be in the JSONL file.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), rec1.ID)
	assert.Contains(t, string(data), rec2.ID)
}

func TestDCRError_Error(t *testing.T) {
	err := &DCRError{Code: "invalid_redirect_uri", Description: "bad uri"}
	assert.Equal(t, "invalid_redirect_uri: bad uri", err.Error())
}
