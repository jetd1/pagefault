// This file implements the public HTTP surface for the 0.7.0 OAuth2
// provider: two discovery endpoints (RFC 9728 + RFC 8414) and the
// token endpoint (RFC 6749 §4.4 client_credentials grant). The
// handlers are mounted outside the auth middleware because:
//
//   - The discovery endpoints MUST be publicly reachable so MCP
//     clients can bootstrap before they have a token.
//   - The token endpoint authenticates via client credentials
//     (either HTTP Basic or form body), not via a bearer token, so
//     routing it through the bearer middleware would reject every
//     legitimate request with a 401.
//
// The provider itself lives in internal/auth.OAuth2Provider — this
// file is a thin adapter that translates HTTP forms into calls on
// OAuth2Provider.IssueToken and JSON-encodes the results.
package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// oauthError is the RFC 6749 §5.2 error envelope emitted by the
// token endpoint on credential / grant failures. Fields are
// snake_case to match the spec verbatim — the top-level errorEnvelope
// used elsewhere in the server doesn't apply here because OAuth2
// clients branch on the spec'd `error` code and will not parse a
// pagefault-specific `error.code`.
type oauthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

// writeOAuthError emits a 400 (or the given status) with the OAuth2
// error envelope. RFC 6749 §5.2 mandates 400 for most grant errors
// and 401 for invalid_client when the client authenticated via the
// Authorization header.
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(oauthError{Code: code, Description: description})
}

// resolveIssuer returns the absolute URL to advertise as the OAuth2
// issuer. Preference order:
//
//  1. auth.oauth2.issuer explicit override
//  2. server.public_url (the reverse-proxy-facing URL)
//  3. inferred from the incoming request's scheme + host
//
// Path (3) is a best-effort fallback that works for direct access
// but may misreport behind a reverse proxy that rewrites Host
// without also setting X-Forwarded-*. Operators running behind a
// proxy should set one of (1) or (2).
func (s *Server) resolveIssuer(r *http.Request) string {
	if s.cfg.Auth.OAuth2.Issuer != "" {
		return strings.TrimRight(s.cfg.Auth.OAuth2.Issuer, "/")
	}
	if s.cfg.Server.PublicURL != "" {
		return strings.TrimRight(s.cfg.Server.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// Respect a reverse proxy's X-Forwarded-Proto header when
	// present — chi's RealIP middleware already normalises the
	// remote addr but does not touch the scheme.
	if fp := r.Header.Get("X-Forwarded-Proto"); fp != "" {
		scheme = fp
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return scheme + "://" + host
}

// handleOAuthProtectedResource serves RFC 9728 (OAuth Protected
// Resource Metadata). MCP clients hit this endpoint first when
// they get a 401 from a tool call, and follow the
// `authorization_servers` pointer to discover where to exchange
// credentials for a token. pagefault acts as both the protected
// resource (MCP endpoints) and the authorization server in the
// same deployment, so the list contains a single entry pointing
// back at us.
func (s *Server) handleOAuthProtectedResource(w http.ResponseWriter, r *http.Request) {
	issuer := s.resolveIssuer(r)
	body := map[string]any{
		"resource":              issuer + "/mcp",
		"authorization_servers": []string{issuer},
		"resource_name":         "pagefault",
		// Hint to clients that we accept Bearer on the MCP endpoints.
		"bearer_methods_supported": []string{"header"},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

// handleOAuthAuthorizationServer serves RFC 8414 (OAuth 2.0
// Authorization Server Metadata). We advertise exactly the subset
// of OAuth2 we support: client_credentials grant, Basic + POST
// client authentication at the token endpoint, and an empty
// response_types list because we do not implement the
// authorization_code flow. The `mcp` scope is listed as
// `scopes_supported` to match MCP client conventions.
func (s *Server) handleOAuthAuthorizationServer(w http.ResponseWriter, r *http.Request) {
	issuer := s.resolveIssuer(r)
	body := map[string]any{
		"issuer":                                issuer,
		"token_endpoint":                        issuer + "/oauth/token",
		"grant_types_supported":                 []string{"client_credentials"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"response_types_supported":              []string{},
		"scopes_supported":                      s.cfg.Auth.OAuth2.DefaultScopesOrDefault(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

// handleOAuthToken implements the RFC 6749 §4.4 client_credentials
// grant. Credentials may arrive in either of two places:
//
//   - `Authorization: Basic base64(client_id:client_secret)` per §2.3.1,
//     which is what Claude Desktop sends.
//   - `client_id` + `client_secret` form fields in the POST body
//     per §2.3.2, which is what curl examples and many programmatic
//     clients use.
//
// A token endpoint MUST support at least one of these; we support
// both because the cost is trivial and it makes operator testing
// much easier. Per §2.3, if the Authorization header is present
// it takes precedence and we do not fall back to the form fields.
func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if s.oauth2P == nil {
		// Endpoint mounted but no provider configured — should not
		// happen because mountOAuth2 only mounts when the provider
		// is non-nil, but be defensive.
		writeOAuthError(w, http.StatusNotFound, "invalid_request", "oauth2 is not enabled on this server")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "failed to parse request body")
		return
	}
	// RFC 6749 §4.4 requires grant_type to be sent in the
	// application/x-www-form-urlencoded POST body. Reading from
	// r.PostForm (not r.Form) rejects values passed via the URL
	// query string so non-compliant clients see a clear
	// unsupported_grant_type error instead of silently succeeding.
	grant := r.PostForm.Get("grant_type")
	if grant != "client_credentials" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "only client_credentials is supported")
		return
	}

	clientID, clientSecret, authMethod, ok := extractClientCredentials(r)
	if !ok {
		// No credentials at all — challenge with WWW-Authenticate
		// Basic so a confused operator running `curl -v` sees the
		// right hint.
		w.Header().Set("WWW-Authenticate", `Basic realm="pagefault"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "missing client credentials")
		return
	}

	var requestedScopes []string
	if s := strings.TrimSpace(r.PostForm.Get("scope")); s != "" {
		requestedScopes = strings.Fields(s)
	}

	issued, err := s.oauth2P.IssueToken(r.Context(), clientID, clientSecret, requestedScopes)
	if err != nil {
		// Per RFC 6749 §5.2, an invalid_client error MAY be
		// returned with a 401 when the client authenticated via
		// Basic. We follow the spec: 401 + WWW-Authenticate on
		// Basic, 400 on form-body creds.
		status := http.StatusBadRequest
		if authMethod == "basic" {
			status = http.StatusUnauthorized
			w.Header().Set("WWW-Authenticate", `Basic realm="pagefault"`)
		}
		writeOAuthError(w, status, "invalid_client", "client authentication failed")
		return
	}

	resp := map[string]any{
		"access_token": issued.AccessToken,
		"token_type":   "Bearer",
		"expires_in":   int(time.Until(issued.ExpiresAt).Seconds()),
	}
	if len(issued.Scopes) > 0 {
		resp["scope"] = strings.Join(issued.Scopes, " ")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// extractClientCredentials pulls (client_id, client_secret) from
// either the Authorization: Basic header (RFC 6749 §2.3.1) or the
// POST body form fields (§2.3.2). The authMethod return value is
// "basic" or "post" so the caller can set the right error status
// on failure. Returns ok=false when neither source contains both
// fields.
func extractClientCredentials(r *http.Request) (id, secret, authMethod string, ok bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		parts := strings.SplitN(h, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Basic") {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
			if err == nil {
				// Per RFC 6749 §2.3.1 the values are
				// form-urlencoded in the Basic string.
				colon := strings.IndexByte(string(decoded), ':')
				if colon >= 0 {
					rawID := string(decoded[:colon])
					rawSec := string(decoded[colon+1:])
					// Per RFC 6749 §2.3.1 the values are
					// form-urlencoded in the Basic string.
					if unID, err := url.QueryUnescape(rawID); err == nil {
						rawID = unID
					}
					if unSec, err := url.QueryUnescape(rawSec); err == nil {
						rawSec = unSec
					}
					if rawID != "" && rawSec != "" {
						return rawID, rawSec, "basic", true
					}
				}
			}
		}
	}
	id = r.PostForm.Get("client_id")
	secret = r.PostForm.Get("client_secret")
	if id != "" && secret != "" {
		return id, secret, "post", true
	}
	return "", "", "", false
}
