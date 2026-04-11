package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"jetd.one/pagefault/internal/config"
	"jetd.one/pagefault/internal/model"
)

const sampleTokens = `# sample tokens file (comments ok)

{"id": "laptop", "token": "pf_laptop_secret", "label": "Laptop"}
{"id": "phone", "token": "pf_phone_secret", "label": "Phone", "metadata": {"device": "ios"}}
`

func writeTokens(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestNoneAuth_ReturnsAnonymous(t *testing.T) {
	p := NoneAuth{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c, err := p.Authenticate(req)
	require.NoError(t, err)
	assert.Equal(t, "anonymous", c.ID)
}

func TestParseTokensJSONL(t *testing.T) {
	recs, err := ParseTokensJSONL([]byte(sampleTokens))
	require.NoError(t, err)
	require.Len(t, recs, 2)
	assert.Equal(t, "laptop", recs[0].ID)
	assert.Equal(t, "pf_laptop_secret", recs[0].Token)
	assert.Equal(t, "phone", recs[1].ID)
	assert.Equal(t, "ios", recs[1].Metadata["device"])
}

func TestParseTokensJSONL_InvalidJSON(t *testing.T) {
	_, err := ParseTokensJSONL([]byte("not json\n"))
	require.Error(t, err)
}

func TestNewBearerTokenAuth_LoadsTokens(t *testing.T) {
	path := writeTokens(t, sampleTokens)
	b, err := NewBearerTokenAuth(path)
	require.NoError(t, err)
	require.NotNil(t, b)
}

func TestBearerAuth_ValidToken(t *testing.T) {
	path := writeTokens(t, sampleTokens)
	b, err := NewBearerTokenAuth(path)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer pf_laptop_secret")
	c, err := b.Authenticate(req)
	require.NoError(t, err)
	assert.Equal(t, "laptop", c.ID)
	assert.Equal(t, "Laptop", c.Label)
}

func TestBearerAuth_InvalidToken(t *testing.T) {
	path := writeTokens(t, sampleTokens)
	b, err := NewBearerTokenAuth(path)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	_, err = b.Authenticate(req)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrUnauthenticated))
}

func TestBearerAuth_MissingHeader(t *testing.T) {
	path := writeTokens(t, sampleTokens)
	b, err := NewBearerTokenAuth(path)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err = b.Authenticate(req)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrUnauthenticated))
	assert.Contains(t, err.Error(), "missing")
}

func TestBearerAuth_MalformedHeader(t *testing.T) {
	path := writeTokens(t, sampleTokens)
	b, err := NewBearerTokenAuth(path)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Token pf_laptop_secret")
	_, err = b.Authenticate(req)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrUnauthenticated))
}

func TestBearerAuth_CaseInsensitiveBearer(t *testing.T) {
	path := writeTokens(t, sampleTokens)
	b, err := NewBearerTokenAuth(path)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "bearer pf_laptop_secret")
	c, err := b.Authenticate(req)
	require.NoError(t, err)
	assert.Equal(t, "laptop", c.ID)
}

func TestBearerAuth_DuplicateToken(t *testing.T) {
	bad := `{"id":"a","token":"pf_x","label":"a"}
{"id":"b","token":"pf_x","label":"b"}`
	path := writeTokens(t, bad)
	_, err := NewBearerTokenAuth(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestBearerAuth_Reload(t *testing.T) {
	path := writeTokens(t, sampleTokens)
	b, err := NewBearerTokenAuth(path)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer pf_laptop_secret")
	_, err = b.Authenticate(req)
	require.NoError(t, err)

	// Overwrite file with empty content; old token should no longer work.
	require.NoError(t, os.WriteFile(path, []byte(`{"id":"new","token":"pf_new","label":"new"}`), 0o600))
	require.NoError(t, b.Reload())

	_, err = b.Authenticate(req)
	require.Error(t, err)
}

func TestMiddleware_InjectsCaller(t *testing.T) {
	path := writeTokens(t, sampleTokens)
	b, err := NewBearerTokenAuth(path)
	require.NoError(t, err)

	var gotID string
	h := Middleware(b)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := CallerFromContext(r.Context())
		gotID = c.ID
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer pf_laptop_secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "laptop", gotID)
}

func TestMiddleware_RejectsUnauthenticated(t *testing.T) {
	path := writeTokens(t, sampleTokens)
	b, err := NewBearerTokenAuth(path)
	require.NoError(t, err)

	called := false
	h := Middleware(b)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, called, "inner handler should not run on auth failure")
	assert.Contains(t, rec.Body.String(), "unauthenticated")
}

func TestMiddleware_PreflightBypassesAuth(t *testing.T) {
	path := writeTokens(t, sampleTokens)
	b, err := NewBearerTokenAuth(path)
	require.NoError(t, err)

	called := false
	h := Middleware(b)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	// OPTIONS with Access-Control-Request-Method and NO Authorization header.
	// Without the preflight bypass this would 401.
	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	req.Header.Set("Origin", "https://claude.ai")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.True(t, called, "preflight must pass through to next handler")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMiddleware_PlainOPTIONSStillRequiresAuth(t *testing.T) {
	// A plain OPTIONS request (no Access-Control-Request-Method header)
	// is NOT a CORS preflight and should still go through auth.
	path := writeTokens(t, sampleTokens)
	b, err := NewBearerTokenAuth(path)
	require.NoError(t, err)

	h := Middleware(b)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"plain OPTIONS without ACRM header must be authenticated")
}

func TestCallerFromContext_DefaultAnonymous(t *testing.T) {
	c := CallerFromContext(nil)
	assert.Equal(t, "anonymous", c.ID)
}

func TestTrustedHeaderAuth_Allow(t *testing.T) {
	a, err := NewTrustedHeaderAuth(config.TrustedHeaderConfig{
		Header: "X-User",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User", "alice")
	c, err := a.Authenticate(req)
	require.NoError(t, err)
	assert.Equal(t, "alice", c.ID)
}

func TestTrustedHeaderAuth_MissingHeader(t *testing.T) {
	a, err := NewTrustedHeaderAuth(config.TrustedHeaderConfig{
		Header: "X-User",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err = a.Authenticate(req)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrUnauthenticated))
}

func TestTrustedHeaderAuth_UntrustedProxy(t *testing.T) {
	a, err := NewTrustedHeaderAuth(config.TrustedHeaderConfig{
		Header:         "X-User",
		TrustedProxies: []string{"10.0.0.1"},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User", "alice")
	req.RemoteAddr = "192.168.1.1:1234"
	_, err = a.Authenticate(req)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrForbidden))
}

func TestGenerateToken(t *testing.T) {
	tok, err := GenerateToken()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(tok, "pf_"))
	assert.Greater(t, len(tok), 20)

	tok2, err := GenerateToken()
	require.NoError(t, err)
	assert.NotEqual(t, tok, tok2, "tokens must be unique")
}

func TestNewProvider_None(t *testing.T) {
	p, err := NewProvider(config.AuthConfig{Mode: "none"})
	require.NoError(t, err)
	_, ok := p.(NoneAuth)
	assert.True(t, ok)
}

func TestNewProvider_Bearer(t *testing.T) {
	path := writeTokens(t, sampleTokens)
	p, err := NewProvider(config.AuthConfig{
		Mode:   "bearer",
		Bearer: config.BearerAuthConfig{TokensFile: path},
	})
	require.NoError(t, err)
	_, ok := p.(*BearerTokenAuth)
	assert.True(t, ok)
}

func TestNewProvider_UnknownMode(t *testing.T) {
	_, err := NewProvider(config.AuthConfig{Mode: "oauth-wow"})
	require.Error(t, err)
}
