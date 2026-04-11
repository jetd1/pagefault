package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
