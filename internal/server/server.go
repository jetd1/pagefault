// Package server wires pagefault's dispatcher, auth, and tool handlers into
// an HTTP server that exposes both an MCP transport (at /mcp, plus the
// legacy-SSE pair at /sse + /message) and a REST transport (at
// /api/{tool_name}).
//
// The server is a thin adapter layer — all real work happens in the
// dispatcher and tool packages. This file is responsible for:
//
//   - Building a chi router with the correct middleware stack
//   - Mounting the mcp-go streamable-http handler on /mcp
//   - Mounting the mcp-go legacy-SSE handler on /sse + /message
//     (for Claude Desktop and other SSE-only clients — the two
//     transports share the same MCPServer and tool set)
//   - Mounting per-tool REST handlers on /api/{tool_name}
//   - Reporting backend health on /health
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"jetd.one/pagefault/internal/auth"
	"jetd.one/pagefault/internal/backend"
	"jetd.one/pagefault/internal/config"
	"jetd.one/pagefault/internal/dispatcher"
	"jetd.one/pagefault/internal/model"
	"jetd.one/pagefault/internal/tool"
	"jetd.one/pagefault/web"
)

// versionSentinel is the placeholder the embedded landing page
// (web/index.html) uses for the running binary's version string.
// Rendered once at server startup against [Version] so the badge
// in the nav, the `pagefault --version` line in the quickstart
// snippet, and the footer chip can never drift from VERSION on
// a release bump. Governed by docs/design.md §11.
const versionSentinel = "{{version}}"

// Version is injected by cmd/pagefault so the /health endpoint can report it.
var Version = "dev"

// Server wraps an http.Handler built from a config, a dispatcher, and an
// auth provider. Callers typically use Run, but the Handler field is
// exposed for integration tests using httptest.
type Server struct {
	cfg        *config.Config
	dispatcher *dispatcher.ToolDispatcher
	authP      auth.AuthProvider
	// oauth2P is the same provider as authP when auth.mode is
	// "oauth2", stashed as a concrete type so the token endpoint
	// handler can call IssueToken without a type assertion on
	// every request. Nil when OAuth2 is not the active mode.
	oauth2P *auth.OAuth2Provider
	mcpSrv  *mcpserver.MCPServer
	sseSrv  *mcpserver.SSEServer

	Handler http.Handler
}

// New constructs a Server from the given config, dispatcher, and auth
// provider. The resulting Server exposes Handler for HTTP requests.
func New(cfg *config.Config, d *dispatcher.ToolDispatcher, authP auth.AuthProvider) (*Server, error) {
	if cfg == nil {
		return nil, errors.New("server: nil config")
	}
	if d == nil {
		return nil, errors.New("server: nil dispatcher")
	}
	if authP == nil {
		return nil, errors.New("server: nil auth provider")
	}

	// Resolve the instructions string now so both transports see the
	// same value. Operators can override the built-in default via
	// server.mcp.instructions in the YAML config; see
	// internal/tool/instructions.go for the default text and the
	// rationale.
	instructions := cfg.Server.MCP.Instructions
	if instructions == "" {
		instructions = tool.DefaultInstructions
	}

	mcpSrv := mcpserver.NewMCPServer(
		"pagefault",
		Version,
		mcpserver.WithToolCapabilities(true),
		mcpserver.WithInstructions(instructions),
	)
	tool.RegisterMCP(mcpSrv, d)

	streamable := mcpserver.NewStreamableHTTPServer(mcpSrv,
		mcpserver.WithEndpointPath("/mcp"),
	)

	// SSE transport is opt-out. Claude Desktop (as of 2026-04) only
	// speaks legacy-SSE, so we default to mounting /sse + /message
	// alongside /mcp. Both transports share the same MCPServer, so
	// the tool set, instructions, and auth chain are identical —
	// the only difference is the wire framing.
	var sseSrv *mcpserver.SSEServer
	if cfg.Server.MCP.SSEEnabledOrDefault() {
		sseOpts := []mcpserver.SSEOption{}
		// WithBaseURL makes the endpoint event an absolute URL, which
		// is safer behind reverse proxies where relative resolution
		// might land the client on the wrong host. When public_url
		// is empty we leave the endpoint event as a root-relative
		// path and let the client resolve it against the URL it was
		// pointed at — same behaviour as before this commit.
		if cfg.Server.PublicURL != "" {
			sseOpts = append(sseOpts, mcpserver.WithBaseURL(cfg.Server.PublicURL))
		}
		// Keepalive pings keep the persistent GET /sse stream from
		// going idle during a long pf_fault call. Without this,
		// intermediate proxies (nginx proxy_read_timeout default 60s,
		// Node undici headersTimeout 60s, Cloudflare free 100s, …)
		// close the connection while the subagent is still running,
		// and the caller sees a "几十秒就挂" failure well before the
		// configured timeout_seconds fires. Opt-out via
		// server.mcp.sse_keepalive: false.
		if cfg.Server.MCP.SSEKeepAliveOrDefault() {
			interval := time.Duration(cfg.Server.MCP.SSEKeepAliveIntervalOrDefault()) * time.Second
			sseOpts = append(sseOpts,
				mcpserver.WithKeepAlive(true),
				mcpserver.WithKeepAliveInterval(interval),
			)
		}
		sseSrv = mcpserver.NewSSEServer(mcpSrv, sseOpts...)
	}

	s := &Server{
		cfg:        cfg,
		dispatcher: d,
		authP:      authP,
		mcpSrv:     mcpSrv,
		sseSrv:     sseSrv,
	}
	// Stash the OAuth2 provider as a concrete pointer so the token
	// endpoint can call IssueToken directly. Safe because auth.NewProvider
	// is the only constructor that can return a *OAuth2Provider and
	// it only does so when Mode == "oauth2".
	if op, ok := authP.(*auth.OAuth2Provider); ok {
		s.oauth2P = op
	}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(requestLogger)
	r.Use(corsMiddleware(cfg.Server.CORS))

	// Static landing site served from the embedded `web` package.
	// Governed by docs/design.md. Two layers:
	//
	//   1. GET/HEAD `/` serves index.html with the `{{version}}`
	//      sentinel replaced by the live [Version] string. The
	//      substitution runs once at New() and the resulting
	//      bytes are served by net/http.ServeContent, which
	//      handles Content-Type, Content-Length, Last-Modified,
	//      HEAD, If-Modified-Since, and Range for free. Doing
	//      the substitution at startup (rather than per request)
	//      means every GET is a static byte copy and eliminates
	//      the class of bug where a release bump leaves the
	//      landing-page version badge stale.
	//
	//   2. GET/HEAD `/styles.css`, `/script.js`, `/favicon.svg`,
	//      `/icons.svg` served by net/http.FileServerFS straight
	//      from the embed. Each asset is an explicit GET+HEAD
	//      pair (chi.Get does not imply HEAD the way
	//      net/http.FileServer does) so link-preview crawlers
	//      and proxy probes don't 405. Explicit paths (no
	//      catch-all) so /api/*, /mcp, /sse, and the OAuth2
	//      routes below are never shadowed.
	indexBytes, err := web.Files.ReadFile("index.html")
	if err != nil {
		return nil, fmt.Errorf("server: read embedded index.html: %w", err)
	}
	indexBytes = bytes.ReplaceAll(indexBytes, []byte(versionSentinel), []byte(Version))
	landingModTime := time.Now().UTC()
	// Each request gets its own bytes.Reader because the reader's
	// seek offset is mutated by ServeContent and readers are not
	// concurrency-safe. The underlying []byte is immutable and
	// shared, so this is O(1) per request.
	landingHandler := func(w http.ResponseWriter, req *http.Request) {
		http.ServeContent(w, req, "index.html", landingModTime, bytes.NewReader(indexBytes))
	}

	r.Get("/", landingHandler)
	r.Head("/", landingHandler)

	webAssets := http.FileServerFS(web.Files)
	staticAsset := func(path string) {
		r.Get(path, webAssets.ServeHTTP)
		r.Head(path, webAssets.ServeHTTP)
	}
	staticAsset("/styles.css")
	staticAsset("/script.js")
	staticAsset("/favicon.svg")
	staticAsset("/icons.svg")

	// Public endpoints (no auth).
	r.Get("/health", s.handleHealth)
	// OpenAPI spec is public so importers (ChatGPT Custom GPT Actions, etc.)
	// can fetch it without a bearer token. The spec itself advertises the
	// BearerAuth scheme, so downstream calls to /api/pf_* still require auth.
	r.Get("/api/openapi.json", s.handleOpenAPISpec)
	// OAuth2 discovery + token endpoints (shipped in 0.7.0). The
	// discovery endpoints MUST be public so MCP clients can bootstrap
	// before they have a token; the token endpoint authenticates via
	// client_credentials (Basic or form body), not via a bearer, so
	// it also lives outside the auth middleware. Mounted unconditionally
	// so curl-based operator testing still returns a clean 404 when
	// OAuth2 isn't enabled — the handlers themselves short-circuit.
	if s.oauth2P != nil {
		r.Get("/.well-known/oauth-protected-resource", s.handleOAuthProtectedResource)
		r.Get("/.well-known/oauth-authorization-server", s.handleOAuthAuthorizationServer)
		r.Post("/oauth/token", s.handleOAuthToken)
		r.Get("/oauth/authorize", s.handleOAuthAuthorize)
		r.Post("/oauth/authorize", s.handleOAuthAuthorize)
		// RFC 7591 Dynamic Client Registration. Mounted only when
		// DCR is enabled — it is opt-in because it creates clients
		// without authentication. Claude Desktop's remote connector
		// requires this endpoint to self-register before starting
		// the authorization_code + PKCE flow.
		if s.oauth2P.DCREnabled() {
			r.Post("/register", s.handleOAuthRegister)
		}
	}

	// Authenticated endpoints. Rate limiting runs after auth so the
	// limiter can key on the resolved caller id.
	r.Group(func(pr chi.Router) {
		pr.Use(auth.Middleware(authP))
		pr.Use(rateLimitMiddleware(cfg.Server.RateLimit))

		// MCP transport (streamable-http). The mcp-go handler expects
		// any method (POST/GET/DELETE) on the endpoint path.
		pr.Handle("/mcp", streamable)
		pr.Handle("/mcp/*", streamable)

		// MCP transport (legacy SSE). Mounted only when enabled in
		// config. The SSE handler answers GET with a persistent
		// text/event-stream, sends an initial "endpoint" event that
		// tells the client where to POST subsequent JSON-RPC
		// messages, then streams responses back as message events.
		// The message handler answers POST with 202 Accepted and
		// routes the body through the shared MCPServer; the response
		// comes back on the SSE stream opened by the paired GET.
		if sseSrv != nil {
			pr.Get("/sse", sseSrv.SSEHandler().ServeHTTP)
			pr.Post("/message", sseSrv.MessageHandler().ServeHTTP)
		}

		// REST transport: one handler per enabled tool. The wire names
		// follow the page-fault scheme (pf_maps, pf_load, pf_scan,
		// pf_peek, pf_fault, pf_ps); the handler Go names retain their
		// generic form for developer clarity — see CLAUDE.md for the
		// mapping.
		pr.Route("/api", func(ar chi.Router) {
			if d.ToolEnabled("pf_maps") {
				ar.Post("/pf_maps", restHandler(d, tool.HandleListContexts))
			}
			if d.ToolEnabled("pf_load") {
				ar.Post("/pf_load", restHandler(d, tool.HandleGetContext))
			}
			if d.ToolEnabled("pf_scan") {
				ar.Post("/pf_scan", restHandler(d, tool.HandleSearch))
			}
			if d.ToolEnabled("pf_peek") {
				ar.Post("/pf_peek", restHandler(d, tool.HandleRead))
			}
			if d.ToolEnabled("pf_fault") {
				ar.Post("/pf_fault", restHandler(d, tool.HandleDeepRetrieve))
			}
			if d.ToolEnabled("pf_ps") {
				// pf_ps is polymorphic — empty task_id → list
				// agents (the classic behaviour), set task_id →
				// return the task snapshot (0.10.0 async poll
				// path). The REST adapter cannot use the generic
				// restHandler because the two modes have
				// different output types; psHandler routes
				// explicitly.
				ar.Post("/pf_ps", s.psHandler())
			}
			if d.ToolEnabled("pf_poke") {
				ar.Post("/pf_poke", restHandler(d, tool.HandleWrite))
			}
		})
	})

	s.Handler = r
	return s, nil
}

// ───────────────── handlers ─────────────────

// handleHealth reports overall liveness and per-backend status. Every
// backend that implements [backend.HealthChecker] is probed with a
// short timeout; backends without Health are reported as "ok" (we have
// no better signal without forcing every backend to lie).
//
// Per-backend entries have the shape `{"status": "ok"|"unavailable",
// "error"?: "..."}`. The top-level "status" field is:
//
//   - "ok"          — every backend is ok
//   - "degraded"    — at least one backend is unavailable
//   - "unavailable" — every backend is unavailable
//
// /health always returns HTTP 200 so operators can fetch it cheaply
// from liveness probes; branch on the envelope's top-level "status"
// field for orchestration decisions.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	const probeTimeout = 2 * time.Second
	backends := map[string]any{}
	okCount, badCount := 0, 0

	for _, name := range s.dispatcher.SortedBackendNames() {
		be, ok := s.dispatcher.Backend(name)
		if !ok {
			continue
		}
		entry := map[string]any{"status": "ok"}
		if hc, ok := be.(backend.HealthChecker); ok {
			ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
			if herr := hc.Health(ctx); herr != nil {
				entry["status"] = "unavailable"
				entry["error"] = herr.Error()
				badCount++
			} else {
				okCount++
			}
			cancel()
		} else {
			okCount++
		}
		backends[name] = entry
	}

	overall := "ok"
	switch {
	case badCount > 0 && okCount == 0:
		overall = "unavailable"
	case badCount > 0:
		overall = "degraded"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   overall,
		"version":  Version,
		"backends": backends,
	})
}

// ───────────────── REST handler adapter ─────────────────

// handlerFn is the shape of every pure tool.Handle* function.
type handlerFn[In any, Out any] func(context.Context, *dispatcher.ToolDispatcher, In, model.Caller) (Out, error)

// restHandler adapts a pure handlerFn into an http.HandlerFunc. It handles
// JSON body decoding, caller extraction, error → HTTP status translation,
// and JSON response encoding.
func restHandler[In any, Out any](d *dispatcher.ToolDispatcher, fn handlerFn[In, Out]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in In
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid json body: %w", err))
				return
			}
		}
		caller := auth.CallerFromContext(r.Context())
		out, err := fn(r.Context(), d, in, caller)
		if err != nil {
			writeError(w, errorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// psHandler is the polymorphic REST handler for `pf_ps`. It decodes
// a ListAgentsInput and routes based on the presence of TaskID:
//
//   - empty TaskID → HandleListAgents → {"agents": [...]}
//   - set TaskID   → HandleTaskStatus → DeepRetrieveOutput
//
// The two modes return different JSON shapes; the generic restHandler
// cannot be used because its type parameters would have to resolve
// to one or the other at compile time. Keeping the routing in the
// server layer (rather than in the tool layer) means the pure
// handlers stay single-purpose and the shape-switching stays close
// to the wire.
func (s *Server) psHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in tool.ListAgentsInput
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid json body: %w", err))
				return
			}
		}
		caller := auth.CallerFromContext(r.Context())
		if in.TaskID != "" {
			out, err := tool.HandleTaskStatus(r.Context(), s.dispatcher, in, caller)
			if err != nil {
				writeError(w, errorStatus(err), err)
				return
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
		out, err := tool.HandleListAgents(r.Context(), s.dispatcher, in, caller)
		if err != nil {
			writeError(w, errorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// errorStatus maps dispatcher/model errors to HTTP status codes.
func errorStatus(err error) int {
	switch {
	case errors.Is(err, model.ErrInvalidRequest):
		return http.StatusBadRequest
	case errors.Is(err, model.ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, model.ErrAccessViolation):
		return http.StatusForbidden
	case errors.Is(err, model.ErrResourceNotFound),
		errors.Is(err, model.ErrContextNotFound),
		errors.Is(err, model.ErrBackendNotFound),
		errors.Is(err, model.ErrAgentNotFound):
		return http.StatusNotFound
	case errors.Is(err, model.ErrContentTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, model.ErrBackendUnavailable):
		return http.StatusBadGateway
	case errors.Is(err, model.ErrSubagentTimeout):
		return http.StatusGatewayTimeout
	case errors.Is(err, model.ErrRateLimited):
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// errorCode maps dispatcher/model errors to stable, snake_case codes that
// clients can branch on without parsing the message. The returned code is
// what clients should match against — messages are for humans.
func errorCode(err error) string {
	switch {
	case errors.Is(err, model.ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, model.ErrUnauthenticated):
		return "unauthenticated"
	case errors.Is(err, model.ErrAccessViolation):
		return "access_violation"
	case errors.Is(err, model.ErrResourceNotFound):
		return "resource_not_found"
	case errors.Is(err, model.ErrContextNotFound):
		return "context_not_found"
	case errors.Is(err, model.ErrBackendNotFound):
		return "backend_not_found"
	case errors.Is(err, model.ErrAgentNotFound):
		return "agent_not_found"
	case errors.Is(err, model.ErrContentTooLarge):
		return "content_too_large"
	case errors.Is(err, model.ErrBackendUnavailable):
		return "backend_unavailable"
	case errors.Is(err, model.ErrSubagentTimeout):
		return "subagent_timeout"
	case errors.Is(err, model.ErrRateLimited):
		return "rate_limited"
	default:
		return "internal_error"
	}
}

// writeJSON serializes v to w as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errorEnvelope is the Phase-3 structured error shape. The REST transport
// always emits this envelope for non-2xx responses so clients can branch on
// Code without parsing Message. Fields are intentionally minimal — detail
// goes in Message, not in extra keys.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// writeError writes a structured JSON error envelope with a stable code.
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorEnvelope{
		Error: errorBody{
			Code:    errorCode(err),
			Status:  status,
			Message: err.Error(),
		},
	})
}

// requestLogger is a lightweight slog-backed request logger. It records
// method, path, status, bytes, duration, and remote addr.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}
