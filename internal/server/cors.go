// Package server — CORS middleware.
//
// The CORS middleware is opt-in (config.Server.CORS.Enabled must be true)
// and only emits headers when the incoming Origin is in the configured
// allowlist. The wildcard "*" is permitted for public endpoints but is
// silently downgraded when AllowCredentials is true — CORS forbids "*"
// with credentials, so we echo the origin instead to keep browsers happy.

package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/jet/pagefault/internal/config"
)

// corsMiddleware returns an HTTP middleware that applies CORS headers from
// cfg. When cfg.Enabled is false the middleware is a no-op — no headers
// added, no preflight short-circuit.
func corsMiddleware(cfg config.CORSConfig) func(http.Handler) http.Handler {
	if !cfg.Enabled || len(cfg.AllowedOrigins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	wildcard := false
	for _, o := range cfg.AllowedOrigins {
		if o == "*" {
			wildcard = true
			continue
		}
		allowed[o] = struct{}{}
	}

	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")
	maxAge := strconv.Itoa(cfg.MaxAge)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			originOK := origin != "" && originAllowed(origin, allowed, wildcard)
			if originOK {
				// Echo the specific origin — required when AllowCredentials
				// is true, and harmless when it isn't.
				allowOrigin := origin
				if wildcard && !cfg.AllowCredentials {
					allowOrigin = "*"
				}
				w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
				w.Header().Set("Vary", "Origin")
				if cfg.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if methods != "" {
					w.Header().Set("Access-Control-Allow-Methods", methods)
				}
				if headers != "" {
					w.Header().Set("Access-Control-Allow-Headers", headers)
				}
				if cfg.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", maxAge)
				}
			}

			// Short-circuit preflight requests, but only when the origin
			// is in the allowlist — a disallowed preflight falls through
			// to the downstream chain so chi returns its normal 405 (for
			// POST-only routes). Returning 204 + no headers to a
			// disallowed origin was ambiguous: browsers would reject it,
			// but the downstream auth / route matching was silently
			// skipped, which made OPTIONS an unintentional path-probing
			// channel.
			if originOK && r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// originAllowed reports whether origin appears in the explicit allowlist,
// or whether the wildcard is present.
func originAllowed(origin string, allowed map[string]struct{}, wildcard bool) bool {
	if wildcard {
		return true
	}
	_, ok := allowed[origin]
	return ok
}
