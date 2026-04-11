// This file implements the OAuth2 client_credentials auth provider
// (shipped in 0.7.0). It exists to make pagefault reachable from
// Claude Desktop's built-in MCP SSE configuration, which only accepts
// OAuth2 Client ID / Client Secret credentials — it cannot attach a
// plain `Authorization: Bearer pf_...` header to the SSE GET.
//
// The provider is deliberately minimal: client_credentials grant only,
// opaque access tokens held in memory with a TTL, no refresh flow, no
// PKCE, no dynamic client registration. Clients are registered
// out-of-band via `pagefault oauth-client create`, which appends a
// bcrypt-hashed secret to the configured clients JSONL file.
//
// The provider runs as a **compound** provider: issued OAuth2 access
// tokens are validated first, and — when `auth.bearer.tokens_file` is
// also set — static bearer tokens from the JSONL store are accepted as
// a fallback. This lets operators migrate Claude Desktop to OAuth2
// without breaking Claude Code deployments that still rely on
// long-lived bearer tokens. The fallback is constructed lazily in
// NewOAuth2Provider and shares the existing BearerTokenAuth
// implementation, so audit entries, caller metadata, and the
// WWW-Authenticate response are identical regardless of which
// validator matched.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

// ErrInvalidClient is returned by IssueToken when the client id or
// secret does not match a registered record. Callers translate this
// into the RFC 6749 §5.2 `invalid_client` error on the token endpoint.
var ErrInvalidClient = errors.New("oauth2: invalid client")

// ClientRecord is one line of the OAuth2 clients JSONL file. The
// secret is stored hashed via bcrypt — `pagefault oauth-client create`
// prints the plaintext secret exactly once at creation time and does
// not store it anywhere else.
type ClientRecord struct {
	ID         string         `json:"id"`
	Label      string         `json:"label"`
	SecretHash string         `json:"secret_hash"`
	Scopes     []string       `json:"scopes,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// IssuedToken is one access token currently held in the in-memory
// store. Tokens live for the configured TTL (default 3600s) and are
// removed lazily on lookup and opportunistically on issue.
type IssuedToken struct {
	AccessToken string
	ClientID    string
	Label       string
	Scopes      []string
	ExpiresAt   time.Time
}

// OAuth2Provider implements AuthProvider for the OAuth2 mode. It
// validates bearer tokens against an in-memory access-token store,
// with an optional fallback to BearerTokenAuth for the compound-mode
// case where long-lived tokens coexist with OAuth2-issued ones.
type OAuth2Provider struct {
	clientsPath string

	mu      sync.RWMutex
	clients map[string]ClientRecord // keyed by client id
	issued  map[string]IssuedToken  // keyed by access token string

	ttl           time.Duration
	defaultScopes []string

	fallback AuthProvider // nil unless bearer.tokens_file is also configured
}

// NewOAuth2Provider constructs an OAuth2Provider from an AuthConfig.
// The clients file is loaded eagerly; a missing or empty file returns
// an error because an OAuth2-mode deployment without registered
// clients is almost certainly a misconfiguration.
//
// When cfg.Bearer.TokensFile is also set, a BearerTokenAuth is
// constructed and stashed as a fallback so operators can migrate
// clients one at a time rather than all at once.
func NewOAuth2Provider(cfg config.AuthConfig) (*OAuth2Provider, error) {
	if cfg.OAuth2.ClientsFile == "" {
		return nil, errors.New("auth: oauth2: clients_file is required")
	}
	p := &OAuth2Provider{
		clientsPath:   cfg.OAuth2.ClientsFile,
		clients:       map[string]ClientRecord{},
		issued:        map[string]IssuedToken{},
		ttl:           time.Duration(cfg.OAuth2.AccessTokenTTLOrDefault()) * time.Second,
		defaultScopes: cfg.OAuth2.DefaultScopesOrDefault(),
	}
	if err := p.ReloadClients(); err != nil {
		return nil, err
	}
	if cfg.Bearer.TokensFile != "" {
		fb, err := NewBearerTokenAuth(cfg.Bearer.TokensFile)
		if err != nil {
			return nil, fmt.Errorf("auth: oauth2: bearer fallback: %w", err)
		}
		p.fallback = fb
	}
	return p, nil
}

// ReloadClients re-reads the clients JSONL file. Safe for concurrent
// use with Authenticate and IssueToken.
func (p *OAuth2Provider) ReloadClients() error {
	data, err := os.ReadFile(p.clientsPath)
	if err != nil {
		return fmt.Errorf("auth: oauth2: read %s: %w", p.clientsPath, err)
	}
	recs, err := ParseClientsJSONL(data)
	if err != nil {
		return err
	}
	m := make(map[string]ClientRecord, len(recs))
	for _, r := range recs {
		if r.ID == "" {
			return fmt.Errorf("auth: oauth2: record with empty id in %s", p.clientsPath)
		}
		if r.SecretHash == "" {
			return fmt.Errorf("auth: oauth2: record %q has empty secret_hash", r.ID)
		}
		if _, dup := m[r.ID]; dup {
			return fmt.Errorf("auth: oauth2: duplicate client id %q in %s", r.ID, p.clientsPath)
		}
		m[r.ID] = r
	}
	p.mu.Lock()
	p.clients = m
	p.mu.Unlock()
	return nil
}

// Authenticate validates a bearer token against the OAuth2 issued
// store first, then falls back to the configured BearerTokenAuth
// (if any). Expired OAuth2 tokens are removed lazily on lookup.
func (p *OAuth2Provider) Authenticate(r *http.Request) (*model.Caller, error) {
	tok, err := extractBearer(r)
	if err != nil {
		return nil, err
	}
	if caller, ok := p.lookupIssuedToken(tok); ok {
		return caller, nil
	}
	if p.fallback != nil {
		return p.fallback.Authenticate(r)
	}
	return nil, fmt.Errorf("%w: invalid token", model.ErrUnauthenticated)
}

// lookupIssuedToken returns the caller associated with an OAuth2
// access token, or false if the token is unknown or expired. Expired
// tokens are removed from the store as a side effect.
func (p *OAuth2Provider) lookupIssuedToken(tok string) (*model.Caller, bool) {
	p.mu.RLock()
	it, ok := p.issued[tok]
	p.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if !it.ExpiresAt.IsZero() && time.Now().After(it.ExpiresAt) {
		p.mu.Lock()
		// Re-check under the write lock in case another request
		// already refreshed the entry.
		if cur, still := p.issued[tok]; still && !cur.ExpiresAt.IsZero() && time.Now().After(cur.ExpiresAt) {
			delete(p.issued, tok)
		}
		p.mu.Unlock()
		return nil, false
	}
	return &model.Caller{
		ID:    it.ClientID,
		Label: it.Label,
		Metadata: map[string]any{
			"auth":      "oauth2",
			"scopes":    it.Scopes,
			"expires":   it.ExpiresAt.UTC().Format(time.RFC3339),
			"client_id": it.ClientID,
		},
	}, true
}

// IssueToken verifies a client's credentials and issues a new access
// token. The provided secret is compared against the stored bcrypt
// hash; requestedScopes narrows the issued token's scope set to the
// intersection with the client's configured scopes (or the provider
// default when the client has none).
//
// Returns ErrInvalidClient on any credential mismatch — callers should
// not leak the specific reason (unknown id vs wrong secret) to the
// client, matching RFC 6749 §5.2.
func (p *OAuth2Provider) IssueToken(ctx context.Context, clientID, providedSecret string, requestedScopes []string) (*IssuedToken, error) {
	if clientID == "" || providedSecret == "" {
		return nil, ErrInvalidClient
	}
	p.mu.RLock()
	rec, ok := p.clients[clientID]
	p.mu.RUnlock()
	if !ok {
		return nil, ErrInvalidClient
	}
	// bcrypt.CompareHashAndPassword is constant-time relative to the
	// candidate length, so this does not leak timing information about
	// whether the secret was close to the stored hash.
	if err := bcrypt.CompareHashAndPassword([]byte(rec.SecretHash), []byte(providedSecret)); err != nil {
		return nil, ErrInvalidClient
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Pick the effective scope set: intersection of requested and
	// allowed. Allowed is rec.Scopes when set, otherwise the provider
	// default. An empty requested list means "give me the allowed set".
	allowed := rec.Scopes
	if len(allowed) == 0 {
		allowed = p.defaultScopes
	}
	effective := allowed
	if len(requestedScopes) > 0 {
		filtered := make([]string, 0, len(requestedScopes))
		allowMap := make(map[string]struct{}, len(allowed))
		for _, s := range allowed {
			allowMap[s] = struct{}{}
		}
		for _, s := range requestedScopes {
			if _, ok := allowMap[s]; ok {
				filtered = append(filtered, s)
			}
		}
		effective = filtered
	}

	raw, err := generateAccessToken()
	if err != nil {
		return nil, fmt.Errorf("auth: oauth2: generate access token: %w", err)
	}
	issued := IssuedToken{
		AccessToken: raw,
		ClientID:    rec.ID,
		Label:       rec.Label,
		Scopes:      effective,
		ExpiresAt:   time.Now().Add(p.ttl),
	}
	p.mu.Lock()
	p.sweepExpiredLocked()
	p.issued[raw] = issued
	p.mu.Unlock()
	return &issued, nil
}

// sweepExpiredLocked removes every expired token from the issued
// map. Caller must hold p.mu as a write lock.
func (p *OAuth2Provider) sweepExpiredLocked() {
	now := time.Now()
	for k, it := range p.issued {
		if !it.ExpiresAt.IsZero() && now.After(it.ExpiresAt) {
			delete(p.issued, k)
		}
	}
}

// TTL returns the access-token lifetime in seconds. Used by the
// token endpoint handler to populate the `expires_in` response field.
func (p *OAuth2Provider) TTL() time.Duration {
	return p.ttl
}

// ClientCount returns the number of registered clients. Exported for
// server startup logging and for tests.
func (p *OAuth2Provider) ClientCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.clients)
}

// generateAccessToken returns a cryptographically random access token
// with a "pf_at_" prefix to distinguish it from long-lived bearer
// tokens in audit logs.
func generateAccessToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "pf_at_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// HashClientSecret generates a bcrypt hash of the given secret for
// storage in the clients file. Exported for the CLI subcommand.
func HashClientSecret(secret string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("auth: oauth2: hash secret: %w", err)
	}
	return string(h), nil
}

// GenerateClientSecret returns a cryptographically random client
// secret with a "pf_cs_" prefix, exported for the CLI subcommand.
// The secret is displayed exactly once at creation time and never
// stored in plaintext.
func GenerateClientSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("auth: oauth2: generate client secret: %w", err)
	}
	return "pf_cs_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// ParseClientsJSONL parses an OAuth2 clients JSONL file. Blank lines
// and comment lines (starting with '#') are ignored.
func ParseClientsJSONL(data []byte) ([]ClientRecord, error) {
	var out []ClientRecord
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		var rec ClientRecord
		if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
			return nil, fmt.Errorf("auth: oauth2: parse line %d: %w", i+1, err)
		}
		out = append(out, rec)
	}
	return out, nil
}
