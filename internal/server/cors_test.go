package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"jetd.one/pagefault/internal/config"
)

// corsTestHandler is a throwaway handler used by the CORS middleware tests.
// It returns 200 with a small body when reached.
func corsTestHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func TestCORS_DisabledIsPassThrough(t *testing.T) {
	mw := corsMiddleware(config.CORSConfig{Enabled: false})
	ts := httptest.NewServer(mw(http.HandlerFunc(corsTestHandler)))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"), "disabled CORS must not emit headers")
}

func TestCORS_EchoesAllowedOrigin(t *testing.T) {
	mw := corsMiddleware(config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"https://good.example"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"Content-Type"},
		MaxAge:         600,
	})
	ts := httptest.NewServer(mw(http.HandlerFunc(corsTestHandler)))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Origin", "https://good.example")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "https://good.example", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "Origin", resp.Header.Get("Vary"))
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "POST")
}

func TestCORS_DisallowedOriginGetsNoHeaders(t *testing.T) {
	mw := corsMiddleware(config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"https://good.example"},
	})
	ts := httptest.NewServer(mw(http.HandlerFunc(corsTestHandler)))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Origin", "https://bad.example")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"), "disallowed origin must not be echoed")
}

func TestCORS_PreflightShortCircuits(t *testing.T) {
	mw := corsMiddleware(config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"https://good.example"},
		AllowedMethods: []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders: []string{"Content-Type", "Authorization"},
		MaxAge:         600,
	})
	ts := httptest.NewServer(mw(http.HandlerFunc(corsTestHandler)))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodOptions, ts.URL, nil)
	req.Header.Set("Origin", "https://good.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "https://good.example", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "POST")
	assert.Equal(t, "600", resp.Header.Get("Access-Control-Max-Age"))
}

func TestCORS_WildcardOrigin(t *testing.T) {
	mw := corsMiddleware(config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET"},
	})
	ts := httptest.NewServer(mw(http.HandlerFunc(corsTestHandler)))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Origin", "https://anywhere.example")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Wildcard with no credentials — either "*" or the echoed origin is
	// acceptable. We use the echoed origin for consistency with the
	// allow-credentials path.
	allow := resp.Header.Get("Access-Control-Allow-Origin")
	assert.Contains(t, []string{"*", "https://anywhere.example"}, allow)
}

func TestCORS_EmptyAllowedOriginsDisables(t *testing.T) {
	// Enabled: true but no allowed origins — behaves like disabled.
	mw := corsMiddleware(config.CORSConfig{Enabled: true, AllowedOrigins: nil})
	ts := httptest.NewServer(mw(http.HandlerFunc(corsTestHandler)))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Origin", "https://good.example")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
}

// TestCORS_DisallowedOriginPreflightReturns204 verifies that a preflight
// from a disallowed origin is still short-circuited with 204, but without
// any CORS headers. The browser will reject the response (no ACAO) while
// the server avoids leaking 401/405 responses from downstream handlers.
func TestCORS_DisallowedOriginPreflightReturns204(t *testing.T) {
	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusTeapot)
	})
	mw := corsMiddleware(config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"https://good.example"},
		AllowedMethods: []string{"POST"},
	})
	ts := httptest.NewServer(mw(inner))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodOptions, ts.URL, nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Preflight is short-circuited with 204 — downstream handler is NOT
	// reached. The browser rejects the response because ACAO is missing.
	assert.False(t, reached, "downstream handler must not run for preflight")
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"),
		"no CORS header for a disallowed origin")
}
