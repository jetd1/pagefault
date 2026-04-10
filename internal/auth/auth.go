// Package auth implements authentication for the pagefault HTTP server.
//
// Three providers are supported:
//
//   - NoneAuth returns an anonymous caller for every request (local dev).
//   - BearerTokenAuth validates an Authorization: Bearer header against a
//     JSONL tokens file.
//   - TrustedHeaderAuth reads an authenticated identity from a configured
//     header (behind a reverse proxy that has already authenticated the
//     caller).  Phase 1 ships the first two; trusted-header is provided as a
//     thin stub for integration tests.
//
// The AuthProvider interface lets the server layer swap implementations
// based on config. Middleware wraps an http.Handler and injects the
// resolved Caller into the request context.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey string

const callerContextKey contextKey = "pagefault.caller"

// AuthProvider authenticates an HTTP request and returns the caller.
type AuthProvider interface {
	Authenticate(r *http.Request) (*model.Caller, error)
}

// CallerFromContext extracts the authenticated caller from the request
// context. Returns AnonymousCaller if no caller is present or ctx is nil.
func CallerFromContext(ctx context.Context) model.Caller {
	if ctx == nil {
		return model.AnonymousCaller
	}
	if v := ctx.Value(callerContextKey); v != nil {
		if c, ok := v.(*model.Caller); ok && c != nil {
			return *c
		}
	}
	return model.AnonymousCaller
}

// WithCaller attaches a caller to a context. Exported for tests and tools
// that need to synthesize a context.
func WithCaller(ctx context.Context, c *model.Caller) context.Context {
	return context.WithValue(ctx, callerContextKey, c)
}

// Middleware returns an http.Handler that authenticates every request using
// the given provider. On success the caller is stored on the request context
// and the next handler is invoked. On failure a 401 (bearer) or 403
// (trusted_header) is written.
func Middleware(p AuthProvider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller, err := p.Authenticate(r)
			if err != nil {
				writeAuthError(w, err)
				return
			}
			ctx := WithCaller(r.Context(), caller)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeAuthError serializes an auth error as JSON with an appropriate status.
func writeAuthError(w http.ResponseWriter, err error) {
	status := http.StatusUnauthorized
	if errors.Is(err, ErrForbidden) {
		status = http.StatusForbidden
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer realm=\"pagefault\"")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":   "unauthenticated",
		"message": err.Error(),
	})
}

// ErrForbidden signals an auth error that should map to HTTP 403 rather than
// 401 (e.g., request from an untrusted proxy IP).
var ErrForbidden = errors.New("forbidden")

// ───────────────────────────── NoneAuth ─────────────────────────────

// NoneAuth grants anonymous access to every request. Intended for local dev
// or deployments behind a trusted proxy that handles auth externally.
type NoneAuth struct{}

// Authenticate returns AnonymousCaller for every request.
func (NoneAuth) Authenticate(*http.Request) (*model.Caller, error) {
	c := model.AnonymousCaller
	return &c, nil
}

// ───────────────────────── BearerTokenAuth ─────────────────────────

// TokenRecord is a single entry in the JSONL tokens file.
type TokenRecord struct {
	ID       string         `json:"id"`
	Token    string         `json:"token"`
	Label    string         `json:"label"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// BearerTokenAuth validates Authorization: Bearer <token> headers against a
// JSONL-backed token store.
type BearerTokenAuth struct {
	path   string
	mu     sync.RWMutex
	byTok  map[string]TokenRecord
	loaded bool
}

// NewBearerTokenAuth constructs a BearerTokenAuth from the given tokens file.
// The file is loaded eagerly; missing or empty files return an error (a
// production setup without tokens is almost certainly a misconfiguration).
func NewBearerTokenAuth(tokensFile string) (*BearerTokenAuth, error) {
	if tokensFile == "" {
		return nil, errors.New("auth: bearer: tokens_file is required")
	}
	b := &BearerTokenAuth{path: tokensFile}
	if err := b.Reload(); err != nil {
		return nil, err
	}
	return b, nil
}

// Reload re-reads the tokens file. Safe for concurrent use.
func (b *BearerTokenAuth) Reload() error {
	data, err := os.ReadFile(b.path)
	if err != nil {
		return fmt.Errorf("auth: bearer: read %s: %w", b.path, err)
	}
	recs, err := ParseTokensJSONL(data)
	if err != nil {
		return err
	}
	m := make(map[string]TokenRecord, len(recs))
	for _, r := range recs {
		if r.Token == "" {
			return fmt.Errorf("auth: bearer: record %q has empty token", r.ID)
		}
		if _, dup := m[r.Token]; dup {
			return fmt.Errorf("auth: bearer: duplicate token in %s", b.path)
		}
		m[r.Token] = r
	}
	b.mu.Lock()
	b.byTok = m
	b.loaded = true
	b.mu.Unlock()
	return nil
}

// Authenticate parses the Authorization header and looks up the caller.
func (b *BearerTokenAuth) Authenticate(r *http.Request) (*model.Caller, error) {
	tok, err := extractBearer(r)
	if err != nil {
		return nil, err
	}
	b.mu.RLock()
	rec, ok := b.byTok[tok]
	b.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: invalid token", model.ErrUnauthenticated)
	}
	return &model.Caller{
		ID:       rec.ID,
		Label:    rec.Label,
		Metadata: rec.Metadata,
	}, nil
}

// extractBearer pulls the token from an Authorization header. Accepts either
// "Bearer <tok>" or "bearer <tok>".
func extractBearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", fmt.Errorf("%w: missing Authorization header", model.ErrUnauthenticated)
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", fmt.Errorf("%w: malformed Authorization header", model.ErrUnauthenticated)
	}
	tok := strings.TrimSpace(parts[1])
	if tok == "" {
		return "", fmt.Errorf("%w: empty token", model.ErrUnauthenticated)
	}
	return tok, nil
}

// ParseTokensJSONL parses a JSONL token file into a slice of records. Blank
// lines are ignored. Comment lines (starting with '#') are ignored.
func ParseTokensJSONL(data []byte) ([]TokenRecord, error) {
	var out []TokenRecord
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		var rec TokenRecord
		if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
			return nil, fmt.Errorf("auth: tokens: parse line %d: %w", i+1, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// ───────────────── TrustedHeaderAuth (stub for Phase 1) ─────────────────

// TrustedHeaderAuth reads the caller identity from a configured header.
// It optionally requires the request to originate from a trusted proxy IP.
type TrustedHeaderAuth struct {
	header         string
	trustedProxies []string
}

// NewTrustedHeaderAuth constructs a TrustedHeaderAuth.
func NewTrustedHeaderAuth(cfg config.TrustedHeaderConfig) (*TrustedHeaderAuth, error) {
	if cfg.Header == "" {
		return nil, errors.New("auth: trusted_header: header is required")
	}
	return &TrustedHeaderAuth{
		header:         cfg.Header,
		trustedProxies: cfg.TrustedProxies,
	}, nil
}

// Authenticate reads the configured header and (optionally) enforces the
// trusted-proxy allowlist.
func (t *TrustedHeaderAuth) Authenticate(r *http.Request) (*model.Caller, error) {
	if len(t.trustedProxies) > 0 {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		allowed := false
		for _, p := range t.trustedProxies {
			if p == ip {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("%w: remote %s not in trusted_proxies", ErrForbidden, ip)
		}
	}
	id := r.Header.Get(t.header)
	if id == "" {
		return nil, fmt.Errorf("%w: header %q missing", model.ErrUnauthenticated, t.header)
	}
	return &model.Caller{ID: id, Label: id}, nil
}

// ───────────────────── Token generation (CLI helper) ─────────────────────

// GenerateToken returns a cryptographically random token with a "pf_" prefix.
// 32 bytes of randomness → ~43 base64 characters → ~48 chars total.
func GenerateToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	return "pf_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// NewProvider constructs an AuthProvider from the given AuthConfig.
func NewProvider(cfg config.AuthConfig) (AuthProvider, error) {
	switch cfg.Mode {
	case "none":
		return NoneAuth{}, nil
	case "bearer":
		return NewBearerTokenAuth(cfg.Bearer.TokensFile)
	case "trusted_header":
		return NewTrustedHeaderAuth(cfg.TrustedHeader)
	default:
		return nil, fmt.Errorf("auth: unknown mode %q", cfg.Mode)
	}
}
