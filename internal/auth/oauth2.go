// This file implements the OAuth2 auth provider (shipped in 0.7.0,
// extended in 0.8.0, DCR added in 0.9.0). It supports two grant types:
//
//   - client_credentials (0.7.0): machine-to-machine auth where the
//     client authenticates with a pre-registered client_secret.
//   - authorization_code + PKCE (0.8.0): the MCP-standard browser-
//     based flow that Claude Desktop requires. Public clients (no
//     secret) use PKCE to protect the code exchange.
//
// Dynamic client registration (DCR, RFC 7591) is opt-in via
// auth.oauth2.dcr_enabled. When enabled, MCP clients like Claude
// Desktop can self-register as public OAuth2 clients without running
// `pagefault oauth-client create` manually. Operators can also
// pre-register clients via the CLI and paste credentials into the
// MCP client's configuration UI.
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
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

// ErrInvalidGrant is returned by ExchangeAuthorizationCode when the
// authorization code is expired, consumed, or otherwise invalid.
// Callers translate this into the RFC 6749 §5.2 `invalid_grant` error.
var ErrInvalidGrant = errors.New("oauth2: invalid grant")

// ErrDCRDisabled is returned by RegisterClient when DCR is not
// enabled on the provider.
var ErrDCRDisabled = errors.New("oauth2: dynamic client registration is disabled")

// DCRError is an RFC 7591 §3.2.2 error returned when client
// registration validation fails. Code is one of
// "invalid_redirect_uri" or "invalid_client_metadata".
type DCRError struct {
	Code        string
	Description string
}

func (e *DCRError) Error() string { return e.Code + ": " + e.Description }

// DCRRequest represents the parsed body of a POST /register request
// per RFC 7591. Only the fields pagefault cares about are typed;
// unknown fields are silently ignored.
type DCRRequest struct {
	// RedirectURIs is required for authorization_code clients.
	RedirectURIs []string `json:"redirect_uris,omitempty"`
	// GrantTypes defaults to ["authorization_code"] when empty.
	GrantTypes []string `json:"grant_types,omitempty"`
	// ResponseTypes defaults to ["code"] when empty.
	ResponseTypes []string `json:"response_types,omitempty"`
	// TokenEndpointAuthMethod must be "" or "none" (public client).
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method,omitempty"`
	// ClientName is stored as the client label.
	ClientName string `json:"client_name,omitempty"`
	// ClientURI is stored in metadata.
	ClientURI string `json:"client_uri,omitempty"`
	// Scope is a space-separated string (RFC 7591 convention).
	// Provider defaults are applied when empty.
	Scope string `json:"scope,omitempty"`
	// LogoURI, TosURI, PolicyURI, SoftwareID, SoftwareVersion
	// are accepted and stored in metadata.
	LogoURI         string `json:"logo_uri,omitempty"`
	TosURI          string `json:"tos_uri,omitempty"`
	PolicyURI       string `json:"policy_uri,omitempty"`
	SoftwareID      string `json:"software_id,omitempty"`
	SoftwareVersion string `json:"software_version,omitempty"`
}

// ClientRecord is one line of the OAuth2 clients JSONL file. The
// secret is stored hashed via bcrypt — `pagefault oauth-client create`
// prints the plaintext secret exactly once at creation time and does
// not store it anywhere else. Public clients (those using
// authorization_code + PKCE without a secret) have an empty SecretHash
// and at least one RedirectURI.
type ClientRecord struct {
	ID           string         `json:"id"`
	Label        string         `json:"label"`
	SecretHash   string         `json:"secret_hash"`
	Scopes       []string       `json:"scopes,omitempty"`
	RedirectURIs []string       `json:"redirect_uris,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
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

// AuthorizationCode is a short-lived, one-time-use authorization code
// issued by the authorization endpoint (GET /oauth/authorize). Codes
// live for the configured AuthCodeTTL (default 60s), can only be
// exchanged once (Consumed is set on first use), and are bound to a
// specific client_id, redirect_uri, and PKCE code_challenge.
type AuthorizationCode struct {
	Code                string
	ClientID            string
	RedirectURI         string
	Scopes              []string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	ExpiresAt           time.Time
	Consumed            bool
}

// OAuth2Provider implements AuthProvider for the OAuth2 mode. It
// validates bearer tokens against an in-memory access-token store,
// with an optional fallback to BearerTokenAuth for the compound-mode
// case where long-lived tokens coexist with OAuth2-issued ones.
type OAuth2Provider struct {
	clientsPath string

	mu        sync.RWMutex
	clients   map[string]ClientRecord      // keyed by client id
	issued    map[string]IssuedToken       // keyed by access token string
	authCodes map[string]AuthorizationCode // keyed by code string

	ttl           time.Duration
	authCodeTTL   time.Duration
	defaultScopes []string
	autoApprove   bool

	dcrEnabled     bool
	dcrBearerToken string

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
		clientsPath:    cfg.OAuth2.ClientsFile,
		clients:        map[string]ClientRecord{},
		issued:         map[string]IssuedToken{},
		authCodes:      map[string]AuthorizationCode{},
		ttl:            time.Duration(cfg.OAuth2.AccessTokenTTLOrDefault()) * time.Second,
		authCodeTTL:    time.Duration(cfg.OAuth2.AuthCodeTTLOrDefault()) * time.Second,
		defaultScopes:  cfg.OAuth2.DefaultScopesOrDefault(),
		autoApprove:    cfg.OAuth2.AutoApproveOrDefault(),
		dcrEnabled:     cfg.OAuth2.DCREnabledOrDefault(),
		dcrBearerToken: cfg.OAuth2.DCRBearerToken,
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

// ReloadClients re-reads the clients JSONL file and sweeps any
// issued access tokens whose owning client is no longer in the
// reloaded set. Safe for concurrent use with Authenticate and
// IssueToken.
//
// The sweep is the mechanism that makes file-based revocation
// (`pagefault oauth-client revoke` → rewrite JSONL → reload) cut
// off an already-authenticated client in one shot. The CLI runs
// out-of-process today, so revocation still needs a restart or a
// future in-process reload signal to trigger this path; see the
// Phase 5 TODO in cmd/pagefault/oauth_client.go.
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
		// Confidential clients must have a secret_hash. Public clients
		// (those using authorization_code + PKCE) may have an empty
		// secret_hash but must have at least one redirect_uri.
		if r.SecretHash == "" && len(r.RedirectURIs) == 0 {
			return fmt.Errorf("auth: oauth2: record %q has empty secret_hash and no redirect_uris (must be either confidential or public)", r.ID)
		}
		if _, dup := m[r.ID]; dup {
			return fmt.Errorf("auth: oauth2: duplicate client id %q in %s", r.ID, p.clientsPath)
		}
		m[r.ID] = r
	}
	p.mu.Lock()
	p.clients = m
	// Sweep issued tokens whose owning client has disappeared from
	// the reloaded file. Tokens for still-present clients are kept
	// so a reload that adds/edits unrelated records does not
	// invalidate active sessions.
	for tok, it := range p.issued {
		if _, still := m[it.ClientID]; !still {
			delete(p.issued, tok)
		}
	}
	p.mu.Unlock()
	return nil
}

// RevokeClient removes an in-memory client record and revokes every
// access token currently issued to that client. Returns the number
// of access tokens purged, which is useful for admin-endpoint
// responses and audit logs.
//
// This does NOT rewrite the clients JSONL file — the caller is
// responsible for persisting the revocation separately (see
// `pagefault oauth-client revoke`). The method exists so the
// running server can be told to forget a client in-process (e.g.
// via a future SIGHUP reload or admin endpoint) without waiting
// for the access_token TTL to expire.
func (p *OAuth2Provider) RevokeClient(clientID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.clients, clientID)
	n := 0
	for tok, it := range p.issued {
		if it.ClientID == clientID {
			delete(p.issued, tok)
			n++
		}
	}
	return n
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
// map and every expired authorization code from the authCodes map.
// Caller must hold p.mu as a write lock.
func (p *OAuth2Provider) sweepExpiredLocked() {
	now := time.Now()
	for k, it := range p.issued {
		if !it.ExpiresAt.IsZero() && now.After(it.ExpiresAt) {
			delete(p.issued, k)
		}
	}
	for k, ac := range p.authCodes {
		if !ac.ExpiresAt.IsZero() && now.After(ac.ExpiresAt) {
			delete(p.authCodes, k)
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

// LookupClient returns the ClientRecord for the given client ID, or
// false if no such client is registered. Exported so the authorize
// endpoint can validate client_id and look up redirect URIs.
func (p *OAuth2Provider) LookupClient(clientID string) (ClientRecord, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	rec, ok := p.clients[clientID]
	return rec, ok
}

// AutoApprove returns whether the provider is configured to skip the
// consent page on the authorization endpoint.
func (p *OAuth2Provider) AutoApprove() bool {
	return p.autoApprove
}

// DCREnabled returns whether dynamic client registration is enabled
// on this provider.
func (p *OAuth2Provider) DCREnabled() bool {
	return p.dcrEnabled
}

// DCRBearerToken returns the configured DCR bearer token, or empty
// string if open registration is allowed.
func (p *OAuth2Provider) DCRBearerToken() string {
	return p.dcrBearerToken
}

// ValidateClientSecret checks if the provided secret matches the
// stored hash for the given client. Returns true if the client is
// confidential and the secret matches. Returns false if the client
// is public (no secret_hash) or the secret doesn't match. Used by
// the authorization_code token exchange to authenticate confidential
// clients without issuing a new client_credentials token.
func (p *OAuth2Provider) ValidateClientSecret(clientID, providedSecret string) bool {
	p.mu.RLock()
	rec, ok := p.clients[clientID]
	p.mu.RUnlock()
	if !ok || rec.SecretHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(rec.SecretHash), []byte(providedSecret)) == nil
}

// IssueAuthorizationCode validates the request parameters and issues a
// new authorization code bound to the given client, redirect URI, and
// PKCE code challenge. The code is stored in memory with the
// configured AuthCodeTTL and can only be exchanged once.
//
// The caller (the HTTP handler) is responsible for validating
// response_type, state presence, and code_challenge_method before
// calling this method — it only checks that the client exists and the
// redirect_uri is registered.
func (p *OAuth2Provider) IssueAuthorizationCode(clientID, redirectURI string, scopes []string, state, codeChallenge, codeChallengeMethod string) (*AuthorizationCode, error) {
	p.mu.RLock()
	rec, ok := p.clients[clientID]
	p.mu.RUnlock()
	if !ok {
		return nil, ErrInvalidClient
	}
	// A client that was registered for client_credentials only (no
	// redirect_uris) has no business on the authorize endpoint.
	// Reject up front regardless of whether the caller passed a
	// redirectURI — the HTTP handler already enforces this, but
	// IssueAuthorizationCode is exported so library callers must
	// get the same guarantee.
	if len(rec.RedirectURIs) == 0 {
		return nil, fmt.Errorf("%w: client %q has no registered redirect_uris", model.ErrInvalidRequest, clientID)
	}
	// Validate redirect_uri exactly matches one of the registered
	// URIs (RFC 6749 §3.1.2.3 exact-match recommendation).
	found := false
	for _, ru := range rec.RedirectURIs {
		if ru == redirectURI {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("%w: redirect_uri not registered for client %q", model.ErrInvalidRequest, clientID)
	}

	// Pick effective scopes.
	allowed := rec.Scopes
	if len(allowed) == 0 {
		allowed = p.defaultScopes
	}
	effective := allowed
	if len(scopes) > 0 {
		filtered := make([]string, 0, len(scopes))
		allowMap := make(map[string]struct{}, len(allowed))
		for _, s := range allowed {
			allowMap[s] = struct{}{}
		}
		for _, s := range scopes {
			if _, ok := allowMap[s]; ok {
				filtered = append(filtered, s)
			}
		}
		effective = filtered
	}

	code, err := generateAuthCode()
	if err != nil {
		return nil, fmt.Errorf("auth: oauth2: generate auth code: %w", err)
	}

	ac := AuthorizationCode{
		Code:                code,
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		Scopes:              effective,
		State:               state,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		ExpiresAt:           time.Now().Add(p.authCodeTTL),
	}

	p.mu.Lock()
	p.sweepExpiredLocked()
	p.authCodes[code] = ac
	p.mu.Unlock()

	return &ac, nil
}

// ExchangeAuthorizationCode validates and consumes an authorization
// code, verifies PKCE, and issues an access token. The code is marked
// as consumed (one-time use) regardless of whether PKCE verification
// succeeds — this prevents replay attacks.
func (p *OAuth2Provider) ExchangeAuthorizationCode(code, redirectURI, clientID, codeVerifier string) (*IssuedToken, error) {
	p.mu.Lock()
	ac, ok := p.authCodes[code]
	if !ok {
		p.mu.Unlock()
		return nil, ErrInvalidGrant
	}
	// Mark consumed immediately to prevent replay, even if
	// subsequent checks fail.
	if ac.Consumed {
		p.mu.Unlock()
		return nil, ErrInvalidGrant
	}
	ac.Consumed = true
	p.authCodes[code] = ac

	// Check expiry.
	if !ac.ExpiresAt.IsZero() && time.Now().After(ac.ExpiresAt) {
		p.mu.Unlock()
		return nil, ErrInvalidGrant
	}
	p.mu.Unlock()

	// Validate client_id matches.
	if ac.ClientID != clientID {
		return nil, ErrInvalidGrant
	}
	// Validate redirect_uri matches.
	if ac.RedirectURI != redirectURI {
		return nil, ErrInvalidGrant
	}
	// Verify PKCE code_challenge.
	if ac.CodeChallenge != "" {
		if !verifyCodeChallenge(codeVerifier, ac.CodeChallenge) {
			return nil, ErrInvalidGrant
		}
	}

	// Look up the client record to get the label for the issued token.
	p.mu.RLock()
	rec, ok := p.clients[clientID]
	p.mu.RUnlock()
	if !ok {
		return nil, ErrInvalidClient
	}

	// For confidential clients, authenticate the client at the token
	// endpoint. The HTTP handler extracts client_secret separately;
	// this method trusts the handler to have already validated it.
	// Public clients (empty SecretHash) skip secret validation.

	raw, err := generateAccessToken()
	if err != nil {
		return nil, fmt.Errorf("auth: oauth2: generate access token: %w", err)
	}
	issued := IssuedToken{
		AccessToken: raw,
		ClientID:    rec.ID,
		Label:       rec.Label,
		Scopes:      ac.Scopes,
		ExpiresAt:   time.Now().Add(p.ttl),
	}
	p.mu.Lock()
	p.sweepExpiredLocked()
	p.issued[raw] = issued
	p.mu.Unlock()
	return &issued, nil
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

// generateAuthCode returns a cryptographically random authorization
// code with a "pf_ac_" prefix to distinguish it from access tokens
// and client secrets in logs.
func generateAuthCode() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "pf_ac_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// verifyCodeChallenge computes BASE64URL(SHA256(codeVerifier)) and
// compares it against the expected challenge using constant-time
// comparison, per RFC 7636 §4.6.
func verifyCodeChallenge(verifier, expectedChallenge string) bool {
	h := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(expectedChallenge)) == 1
}

// GenerateClientID returns a cryptographically random client ID with
// a "pf_dcr_" prefix, suitable for dynamically-registered clients.
// The prefix distinguishes DCR clients from CLI-created clients in
// logs and audit entries. 128 bits of randomness is plenty for a
// client ID — the space is not expected to collide.
func GenerateClientID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("auth: oauth2: generate client id: %w", err)
	}
	return "pf_dcr_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// isLocalhostOrHTTPS reports whether a redirect URI is acceptable for
// MCP clients: localhost (any port), 127.0.0.1, [::1], or HTTPS.
// This prevents open-redirect attacks through non-local HTTP URIs and
// matches the MCP specification's security requirements.
func isLocalhostOrHTTPS(rawURI string) bool {
	u, err := url.Parse(rawURI)
	if err != nil {
		return false
	}
	host := u.Hostname()
	switch {
	case u.Scheme == "https":
		return true
	case host == "localhost", host == "127.0.0.1", host == "::1":
		// https://localhost is already accepted by the case above;
		// the loopback case only needs to additionally allow http.
		// Other schemes (ftp, file, ...) fall through to false.
		return u.Scheme == "http"
	default:
		return false
	}
}

// RegisterClient validates and persists a dynamically-registered
// OAuth2 client per RFC 7591. It creates a public client (no
// client_secret, PKCE-only) and appends the record to the clients
// JSONL file. Returns the new ClientRecord on success.
//
// Validation rules:
//   - redirect_uris is required and must be non-empty
//   - each redirect_uri must be localhost or HTTPS (MCP security convention)
//   - grant_types: "authorization_code" is accepted, "refresh_token" is
//     silently accepted (pagefault does not issue refresh tokens), anything
//     else is rejected
//   - token_endpoint_auth_method, if present, must be "none" (public client)
//   - client_name is stored as the client label
//   - scope, if non-empty, overrides provider defaults
func (p *OAuth2Provider) RegisterClient(req DCRRequest) (*ClientRecord, error) {
	if !p.dcrEnabled {
		return nil, ErrDCRDisabled
	}

	// 1. Validate redirect_uris (required, non-empty, localhost or HTTPS).
	if len(req.RedirectURIs) == 0 {
		return nil, &DCRError{Code: "invalid_redirect_uri", Description: "redirect_uris is required"}
	}
	for _, u := range req.RedirectURIs {
		if !isLocalhostOrHTTPS(u) {
			return nil, &DCRError{
				Code:        "invalid_redirect_uri",
				Description: fmt.Sprintf("redirect_uri %q must be localhost or HTTPS", u),
			}
		}
	}

	// 2. Validate grant_types. "authorization_code" and "refresh_token"
	// are accepted; anything else is rejected.
	for _, gt := range req.GrantTypes {
		switch gt {
		case "authorization_code", "refresh_token":
			// accepted; refresh_token silently ignored
		default:
			return nil, &DCRError{
				Code:        "invalid_client_metadata",
				Description: fmt.Sprintf("unsupported grant_type %q", gt),
			}
		}
	}

	// 3. Validate token_endpoint_auth_method. Only "none" (public client)
	// is supported for DCR — we don't issue client_secrets.
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
		return nil, &DCRError{
			Code:        "invalid_client_metadata",
			Description: "only 'none' token_endpoint_auth_method is supported for dynamic registration (public client, PKCE-only)",
		}
	}

	// 4. Generate client_id.
	clientID, err := GenerateClientID()
	if err != nil {
		return nil, fmt.Errorf("auth: oauth2: dcr: %w", err)
	}

	// 5. Determine effective scopes.
	var scopes []string
	if req.Scope != "" {
		scopes = strings.Fields(req.Scope)
	}
	if len(scopes) == 0 {
		scopes = p.defaultScopes
	}

	// 6. Build the ClientRecord (public client, no secret).
	now := time.Now().UTC()
	rec := ClientRecord{
		ID:           clientID,
		Label:        req.ClientName,
		SecretHash:   "", // public client
		Scopes:       scopes,
		RedirectURIs: req.RedirectURIs,
		Metadata: map[string]any{
			"created_at":                 now.Format(time.RFC3339),
			"dcr":                        true,
			"grant_types":                req.GrantTypes,
			"response_types":             req.ResponseTypes,
			"token_endpoint_auth_method": req.TokenEndpointAuthMethod,
			"client_uri":                 req.ClientURI,
			"logo_uri":                   req.LogoURI,
			"tos_uri":                    req.TosURI,
			"policy_uri":                 req.PolicyURI,
			"software_id":                req.SoftwareID,
			"software_version":           req.SoftwareVersion,
		},
	}

	// 7. Persist: append to the JSONL file.
	if err := p.appendClient(rec); err != nil {
		return nil, fmt.Errorf("auth: oauth2: dcr: persist: %w", err)
	}

	// 8. Add to in-memory map.
	p.mu.Lock()
	p.clients[clientID] = rec
	p.mu.Unlock()

	return &rec, nil
}

// appendClient appends a single ClientRecord as a JSONL line to the
// clients file. Uses O_APPEND|O_CREATE|O_WRONLY plus fsync for
// durability, matching the audit log's write pattern. The append-only
// approach avoids a read-rewrite race with the CLI's writeOAuthClients.
func (p *OAuth2Provider) appendClient(rec ClientRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(p.clientsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return f.Sync()
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
