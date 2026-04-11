package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"jetd.one/pagefault/internal/auth"
	"jetd.one/pagefault/internal/config"
	"jetd.one/pagefault/internal/model"
)

// withCaller wraps a handler so every request sees the given caller on
// its context. Used to drive the rate limiter from tests without
// standing up the full auth middleware.
func withCaller(id string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := model.Caller{ID: id, Label: id}
		ctx := auth.WithCaller(r.Context(), &c)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func rateLimitTestHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func TestRateLimit_DisabledIsPassThrough(t *testing.T) {
	mw := rateLimitMiddleware(config.RateLimitConfig{Enabled: false})
	ts := httptest.NewServer(withCaller("x", mw(http.HandlerFunc(rateLimitTestHandler))))
	defer ts.Close()

	for range 20 {
		resp, err := http.Get(ts.URL)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}
}

func TestRateLimit_Trips429WhenOverBudget(t *testing.T) {
	// Burst of 2, rps of 0.1 — three rapid calls should see 429 on the
	// third (or soon after) because the bucket drains faster than it
	// refills.
	mw := rateLimitMiddleware(config.RateLimitConfig{Enabled: true, RPS: 0.1, Burst: 2})
	ts := httptest.NewServer(withCaller("burst", mw(http.HandlerFunc(rateLimitTestHandler))))
	defer ts.Close()

	var statuses []int
	for range 5 {
		resp, err := http.Get(ts.URL)
		require.NoError(t, err)
		statuses = append(statuses, resp.StatusCode)
		resp.Body.Close()
	}
	saw429 := false
	for _, s := range statuses {
		if s == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	assert.True(t, saw429, "expected at least one 429 in %v", statuses)
}

func TestRateLimit_429EnvelopeShape(t *testing.T) {
	mw := rateLimitMiddleware(config.RateLimitConfig{Enabled: true, RPS: 0.1, Burst: 1})
	ts := httptest.NewServer(withCaller("env", mw(http.HandlerFunc(rateLimitTestHandler))))
	defer ts.Close()

	// First call passes, second should be limited.
	resp, err := http.Get(ts.URL)
	require.NoError(t, err)
	resp.Body.Close()

	resp, err = http.Get(ts.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("Retry-After"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &env))
	assert.Equal(t, "rate_limited", env.Error.Code)
	assert.Equal(t, http.StatusTooManyRequests, env.Error.Status)
}

func TestRateLimit_SeparateCallersHaveSeparateBuckets(t *testing.T) {
	// A single middleware instance (= single bucket registry) serving two
	// different caller identities. Caller A exhausts its bucket; caller B
	// should still be able to pass on the same server.
	mw := rateLimitMiddleware(config.RateLimitConfig{Enabled: true, RPS: 0.1, Burst: 1})
	inner := http.HandlerFunc(rateLimitTestHandler)
	mux := http.NewServeMux()
	mux.Handle("/a", withCaller("caller-a", mw(inner)))
	mux.Handle("/b", withCaller("caller-b", mw(inner)))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// caller-a burns its one token, then gets 429.
	resp, err := http.Get(ts.URL + "/a")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = http.Get(ts.URL + "/a")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// caller-b has its own bucket; the first request should pass.
	resp, err = http.Get(ts.URL + "/b")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
