// This file implements the public HTTP surface for the OAuth2 provider:
// two discovery endpoints (RFC 9728 + RFC 8414), the token endpoint
// (RFC 6749 — client_credentials and authorization_code grants), and
// the authorization endpoint (GET/POST /oauth/authorize for the
// authorization_code + PKCE flow). The handlers are mounted outside
// the auth middleware because:
//
//   - The discovery endpoints MUST be publicly reachable so MCP
//     clients can bootstrap before they have a token.
//   - The token endpoint authenticates via client credentials
//     (either HTTP Basic or form body) or via PKCE code_verifier,
//     not via a bearer token, so routing it through the bearer
//     middleware would reject every legitimate request with a 401.
//   - The authorization endpoint is a browser-facing redirect flow
//     that does not use bearer tokens at all.
//
// The provider itself lives in internal/auth.OAuth2Provider — this
// file is a thin adapter that translates HTTP forms into calls on
// OAuth2Provider methods and JSON-encodes the results.
package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jet/pagefault/internal/auth"
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
// Resource Metadata). MCP clients hit this endpoint first when they
// get a 401 from a tool call, and follow the
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
// Authorization Server Metadata). We advertise both grant types
// (client_credentials and authorization_code), the authorization
// endpoint, and the S256 PKCE code challenge method. The `mcp`
// scope is listed as `scopes_supported` to match MCP client
// conventions.
func (s *Server) handleOAuthAuthorizationServer(w http.ResponseWriter, r *http.Request) {
	issuer := s.resolveIssuer(r)
	body := map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"grant_types_supported":                 []string{"client_credentials", "authorization_code"},
		"response_types_supported":              []string{"code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"scopes_supported":                      s.cfg.Auth.OAuth2.DefaultScopesOrDefault(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

// handleOAuthToken implements the RFC 6749 token endpoint for both
// client_credentials (§4.4) and authorization_code (§4.1) grants.
//
// client_credentials: credentials may arrive in either of two places:
//
//   - `Authorization: Basic base64(client_id:client_secret)` per §2.3.1,
//     which is what Claude Desktop sends.
//   - `client_id` + `client_secret` form fields in the POST body
//     per §2.3.2, which is what curl examples and many programmatic
//     clients use.
//
// authorization_code: the client sends `code`, `redirect_uri`,
// `client_id`, and `code_verifier` (PKCE). Public clients (those
// without a client_secret) authenticate via PKCE alone.
func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if s.oauth2P == nil {
		writeOAuthError(w, http.StatusNotFound, "invalid_request", "oauth2 is not enabled on this server")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "failed to parse request body")
		return
	}

	grant := r.PostForm.Get("grant_type")
	switch grant {
	case "client_credentials":
		s.handleClientCredentialsGrant(w, r)
	case "authorization_code":
		s.handleAuthorizationCodeGrant(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "only client_credentials and authorization_code are supported")
	}
}

// handleClientCredentialsGrant processes the client_credentials
// grant type on the token endpoint.
func (s *Server) handleClientCredentialsGrant(w http.ResponseWriter, r *http.Request) {
	clientID, clientSecret, authMethod, ok := extractClientCredentials(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="pagefault"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "missing client credentials")
		return
	}

	var requestedScopes []string
	if sc := strings.TrimSpace(r.PostForm.Get("scope")); sc != "" {
		requestedScopes = strings.Fields(sc)
	}

	issued, err := s.oauth2P.IssueToken(r.Context(), clientID, clientSecret, requestedScopes)
	if err != nil {
		status := http.StatusBadRequest
		if authMethod == "basic" {
			status = http.StatusUnauthorized
			w.Header().Set("WWW-Authenticate", `Basic realm="pagefault"`)
		}
		writeOAuthError(w, status, "invalid_client", "client authentication failed")
		return
	}

	s.writeTokenResponse(w, issued)
}

// handleAuthorizationCodeGrant processes the authorization_code
// grant type on the token endpoint. Public clients (no client_secret)
// authenticate via PKCE code_verifier alone.
func (s *Server) handleAuthorizationCodeGrant(w http.ResponseWriter, r *http.Request) {
	code := r.PostForm.Get("code")
	redirectURI := r.PostForm.Get("redirect_uri")
	clientID := r.PostForm.Get("client_id")
	codeVerifier := r.PostForm.Get("code_verifier")

	// client_id may also come from the Authorization header (Basic auth).
	// If it's absent from the form body, try extracting from Basic auth.
	if clientID == "" {
		if id, _, _, ok := extractClientCredentials(r); ok {
			clientID = id
		}
	}

	if code == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "missing code parameter")
		return
	}
	if clientID == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "missing client_id parameter")
		return
	}

	// For confidential clients, validate the client_secret.
	// For public clients (PKCE-only), client_secret is absent.
	rec, clientExists := s.oauth2P.LookupClient(clientID)
	if !clientExists {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "unknown client")
		return
	}

	// If the client has a secret_hash, it must authenticate with
	// client_secret. If it's a public client (empty secret_hash),
	// PKCE provides the authentication.
	if rec.SecretHash != "" {
		// Confidential client: extract and validate secret.
		extractedID, secret, authMethod, gotCreds := extractClientCredentials(r)
		if !gotCreds {
			w.Header().Set("WWW-Authenticate", `Basic realm="pagefault"`)
			writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "missing client credentials")
			return
		}
		// Use the ID from extractClientCredentials if present
		// (Basic auth), otherwise fall back to the form field.
		if extractedID != "" {
			clientID = extractedID
		}
		if !s.oauth2P.ValidateClientSecret(clientID, secret) {
			status := http.StatusBadRequest
			if authMethod == "basic" {
				status = http.StatusUnauthorized
				w.Header().Set("WWW-Authenticate", `Basic realm="pagefault"`)
			}
			writeOAuthError(w, status, "invalid_client", "client authentication failed")
			return
		}
	}

	issued, err := s.oauth2P.ExchangeAuthorizationCode(code, redirectURI, clientID, codeVerifier)
	if err != nil {
		switch {
		case isErrInvalidGrant(err):
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", err.Error())
		case isErrInvalidClient(err):
			writeOAuthError(w, http.StatusBadRequest, "invalid_client", err.Error())
		default:
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code exchange failed")
		}
		return
	}

	s.writeTokenResponse(w, issued)
}

// writeTokenResponse writes a standard OAuth2 token success response.
func (s *Server) writeTokenResponse(w http.ResponseWriter, issued *auth.IssuedToken) {
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

// handleOAuthAuthorize implements the authorization endpoint for
// the authorization_code + PKCE flow (RFC 6749 §4.1 + RFC 7636).
// It handles both GET (initial request from the client, typically
// via browser redirect) and POST (consent form submission when
// auto_approve is false).
//
// When auto_approve is true (the default), the endpoint immediately
// issues an authorization code and redirects to the client's
// redirect_uri. When false, a minimal HTML consent page is rendered.
func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	if s.oauth2P == nil {
		writeOAuthError(w, http.StatusNotFound, "invalid_request", "oauth2 is not enabled on this server")
		return
	}

	// For GET, params come from the query string. For POST (consent
	// form), params come from the form body.
	var responseType, clientID, redirectURI, scope, state, codeChallenge, codeChallengeMethod string
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "failed to parse request body")
			return
		}
		responseType = r.PostForm.Get("response_type")
		clientID = r.PostForm.Get("client_id")
		redirectURI = r.PostForm.Get("redirect_uri")
		scope = r.PostForm.Get("scope")
		state = r.PostForm.Get("state")
		codeChallenge = r.PostForm.Get("code_challenge")
		codeChallengeMethod = r.PostForm.Get("code_challenge_method")
	} else {
		responseType = r.URL.Query().Get("response_type")
		clientID = r.URL.Query().Get("client_id")
		redirectURI = r.URL.Query().Get("redirect_uri")
		scope = r.URL.Query().Get("scope")
		state = r.URL.Query().Get("state")
		codeChallenge = r.URL.Query().Get("code_challenge")
		codeChallengeMethod = r.URL.Query().Get("code_challenge_method")
	}

	// Validate required parameters.
	if responseType != "code" {
		s.authorizeError(w, r, redirectURI, state, "unsupported_response_type", "only 'code' response_type is supported")
		return
	}
	if clientID == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "missing client_id")
		return
	}
	rec, ok := s.oauth2P.LookupClient(clientID)
	if !ok {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "unknown client")
		return
	}
	if redirectURI == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "missing redirect_uri")
		return
	}
	// Validate redirect_uri is registered.
	if len(rec.RedirectURIs) > 0 {
		found := false
		for _, ru := range rec.RedirectURIs {
			if ru == redirectURI {
				found = true
				break
			}
		}
		if !found {
			// Don't redirect with the error — the redirect_uri is
			// not registered, so redirecting would be unsafe.
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "redirect_uri not registered for this client")
			return
		}
	}
	if state == "" {
		s.authorizeError(w, r, redirectURI, state, "invalid_request", "missing state parameter")
		return
	}
	if codeChallenge == "" {
		s.authorizeError(w, r, redirectURI, state, "invalid_request", "missing code_challenge (PKCE is required)")
		return
	}
	if codeChallengeMethod != "S256" {
		s.authorizeError(w, r, redirectURI, state, "invalid_request", "only S256 code_challenge_method is supported")
		return
	}

	// If auto_approve is false and this is a GET, render the consent
	// page. POST with action=allow proceeds; POST with action=deny
	// redirects with access_denied.
	if !s.oauth2P.AutoApprove() {
		if r.Method == http.MethodGet {
			s.renderConsentPage(w, rec, r.URL.Query())
			return
		}
		if r.Method == http.MethodPost && r.PostForm.Get("action") == "deny" {
			s.authorizeError(w, r, redirectURI, state, "access_denied", "resource owner denied the request")
			return
		}
	}

	// Parse requested scopes.
	var scopes []string
	if scope != "" {
		scopes = strings.Fields(scope)
	}

	// Issue the authorization code.
	ac, err := s.oauth2P.IssueAuthorizationCode(clientID, redirectURI, scopes, state, codeChallenge, codeChallengeMethod)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to issue authorization code")
		return
	}

	// Redirect to the client's redirect_uri with code and state.
	redirectTo := buildRedirectURI(redirectURI, ac.Code, state)
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

// authorizeError either redirects to the client's redirect_uri with
// the error (when the redirect_uri and state are valid) or writes a
// JSON error response (when redirecting would be unsafe).
func (s *Server) authorizeError(w http.ResponseWriter, r *http.Request, redirectURI, state, errorCode, description string) {
	if redirectURI != "" && state != "" {
		u, err := url.Parse(redirectURI)
		if err == nil {
			q := u.Query()
			q.Set("error", errorCode)
			q.Set("error_description", description)
			if state != "" {
				q.Set("state", state)
			}
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.String(), http.StatusFound)
			return
		}
	}
	writeOAuthError(w, http.StatusBadRequest, errorCode, description)
}

// buildRedirectURI constructs the redirect URL with code and state.
func buildRedirectURI(base, code, state string) string {
	u, err := url.Parse(base)
	if err != nil {
		// Should not happen — redirect_uri was validated earlier.
		return base + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
	}
	q := u.Query()
	q.Set("code", code)
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String()
}

// renderConsentPage writes a minimal HTML consent page. This is only
// reached when auto_approve is false.
func (s *Server) renderConsentPage(w http.ResponseWriter, rec auth.ClientRecord, params url.Values) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")

	clientName := html.EscapeString(rec.Label)
	if clientName == "" {
		clientName = html.EscapeString(rec.ID)
	}

	// Build hidden form fields preserving all original query params.
	var hiddenFields strings.Builder
	for key, values := range params {
		for _, v := range values {
			hiddenFields.WriteString(fmt.Sprintf(
				`<input type="hidden" name="%s" value="%s">`,
				html.EscapeString(key), html.EscapeString(v),
			))
		}
	}

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Authorize - pagefault</title>
<style>
body{font-family:system-ui,sans-serif;max-width:480px;margin:80px auto;padding:0 20px;color:#1a1a1a}
h1{font-size:1.2em}form{margin-top:24px}
button{padding:8px 24px;margin-right:12px;cursor:pointer;border:1px solid #ccc;border-radius:4px;background:#fff}
button[name=action][value=allow]{background:#1a7f37;color:#fff;border-color:#1a7f37}
button[name=action][value=deny]{background:#fff;color:#cf222e;border-color:#cf222e}
</style>
</head>
<body>
<h1>Authorize %s</h1>
<p>Allow this application to access your pagefault server?</p>
<form method="POST" action="/oauth/authorize">
%s
<button type="submit" name="action" value="allow">Allow</button>
<button type="submit" name="action" value="deny">Deny</button>
</form>
</body>
</html>`, clientName, hiddenFields.String())
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

// isErrInvalidGrant checks if the error is an invalid_grant error.
func isErrInvalidGrant(err error) bool {
	return err.Error() == "oauth2: invalid grant" ||
		strings.Contains(err.Error(), "invalid grant")
}

// isErrInvalidClient checks if the error is an invalid_client error.
func isErrInvalidClient(err error) bool {
	return err.Error() == "oauth2: invalid client" ||
		strings.Contains(err.Error(), "invalid client")
}

// computePKCEChallenge computes BASE64URL(SHA256(verifier)), the S256
// code_challenge method per RFC 7636. Exported for tests.
func computePKCEChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
