// Package server wires pagefault's dispatcher, auth, and tool handlers into
// an HTTP server that exposes both an MCP transport (at /mcp) and a REST
// transport (at /api/{tool_name}).
//
// The server is a thin adapter layer — all real work happens in the
// dispatcher and tool packages. This file is responsible for:
//
//   - Building a chi router with the correct middleware stack
//   - Mounting the mcp-go streamable-http handler on /mcp
//   - Mounting per-tool REST handlers on /api/{tool_name}
//   - Reporting backend health on /health
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jet/pagefault/internal/auth"
	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/model"
	"github.com/jet/pagefault/internal/tool"
)

// Version is injected by cmd/pagefault so the /health endpoint can report it.
var Version = "dev"

// Server wraps an http.Handler built from a config, a dispatcher, and an
// auth provider. Callers typically use Run, but the Handler field is
// exposed for integration tests using httptest.
type Server struct {
	cfg        *config.Config
	dispatcher *dispatcher.ToolDispatcher
	authP      auth.AuthProvider
	mcpSrv     *mcpserver.MCPServer

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

	mcpSrv := mcpserver.NewMCPServer(
		"pagefault",
		Version,
		mcpserver.WithToolCapabilities(true),
	)
	tool.RegisterMCP(mcpSrv, d)

	streamable := mcpserver.NewStreamableHTTPServer(mcpSrv,
		mcpserver.WithEndpointPath("/mcp"),
	)

	s := &Server{
		cfg:        cfg,
		dispatcher: d,
		authP:      authP,
		mcpSrv:     mcpSrv,
	}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(requestLogger)

	// Public endpoints (no auth).
	r.Get("/health", s.handleHealth)
	r.Get("/", s.handleRoot)

	// Authenticated endpoints.
	r.Group(func(pr chi.Router) {
		pr.Use(auth.Middleware(authP))

		// MCP transport. The streamable-http handler expects any method
		// (POST/GET/DELETE) on the endpoint path.
		pr.Handle("/mcp", streamable)
		pr.Handle("/mcp/*", streamable)

		// REST transport: one handler per enabled tool.
		pr.Route("/api", func(ar chi.Router) {
			if d.ToolEnabled("list_contexts") {
				ar.Post("/list_contexts", restHandler(d, tool.HandleListContexts))
			}
			if d.ToolEnabled("get_context") {
				ar.Post("/get_context", restHandler(d, tool.HandleGetContext))
			}
			if d.ToolEnabled("search") {
				ar.Post("/search", restHandler(d, tool.HandleSearch))
			}
			if d.ToolEnabled("read") {
				ar.Post("/read", restHandler(d, tool.HandleRead))
			}
		})
	})

	s.Handler = r
	return s, nil
}

// ───────────────── handlers ─────────────────

// handleHealth reports overall liveness and per-backend status.
//
// Phase 1: all configured backends are reported as "ok" by name. A real
// ping/probe mechanism can be added in a later phase.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	backends := map[string]string{}
	for _, name := range s.dispatcher.SortedBackendNames() {
		backends[name] = "ok"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"version":  Version,
		"backends": backends,
	})
}

// handleRoot is a minimal landing page with links to /health and /mcp.
func (s *Server) handleRoot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "pagefault %s\n\n", Version)
	_, _ = io.WriteString(w, "Endpoints:\n")
	_, _ = io.WriteString(w, "  GET  /health           — health probe\n")
	_, _ = io.WriteString(w, "  POST /mcp              — MCP streamable-http\n")
	_, _ = io.WriteString(w, "  POST /api/list_contexts\n")
	_, _ = io.WriteString(w, "  POST /api/get_context\n")
	_, _ = io.WriteString(w, "  POST /api/search\n")
	_, _ = io.WriteString(w, "  POST /api/read\n")
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
		errors.Is(err, model.ErrBackendNotFound):
		return http.StatusNotFound
	case errors.Is(err, model.ErrBackendUnavailable):
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// writeJSON serializes v to w as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error envelope.
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{
		"error":   strings.ToLower(http.StatusText(status)),
		"message": err.Error(),
	})
}

// requestLogger is a lightweight slog-backed request logger. It records
// method, path, status, and duration.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := r.Context().Value(middleware.RequestIDKey)
		_ = start
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"remote", r.RemoteAddr,
		)
	})
}
