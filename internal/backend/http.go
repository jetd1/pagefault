package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

// HTTPBackend is a generic HTTP-call-and-parse backend. It answers
// Search by POSTing (or GETting, if configured) to a remote endpoint and
// extracting a result array from the JSON response. Read and
// ListResources are unsupported in Phase 2 — operators who need read
// access should expose the same data via a filesystem backend or a
// dedicated backend type.
//
// The request body is rendered from cfg.Search.BodyTemplate with
// {query} and {limit} substituted. The response JSON is walked using
// cfg.Search.ResponsePath (dotted or "$."-prefixed JSONPath-lite).
//
// Each result in the array must be a JSON object; these keys are
// recognised (all optional):
//
//	uri      — string
//	snippet  — string
//	score    — number
//	metadata — object, passed through as-is
type HTTPBackend struct {
	name    string
	baseURL string
	authTok string
	search  config.HTTPBackendRequest
	timeout time.Duration
	client  *http.Client
}

// NewHTTPBackend builds an HTTP backend from config.
func NewHTTPBackend(cfg *config.HTTPBackendConfig) (*HTTPBackend, error) {
	if cfg == nil {
		return nil, errors.New("http backend: nil config")
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("http backend %q: base_url is required", cfg.Name)
	}
	if cfg.Search.Path == "" {
		return nil, fmt.Errorf("http backend %q: search.path is required", cfg.Name)
	}
	timeoutSec := cfg.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 15
	}
	authTok := ""
	if cfg.Auth.Mode == "bearer" {
		authTok = cfg.Auth.Token
	}
	return &HTTPBackend{
		name:    cfg.Name,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		authTok: authTok,
		search:  cfg.Search,
		timeout: time.Duration(timeoutSec) * time.Second,
		client:  &http.Client{},
	}, nil
}

// Name returns the backend name.
func (b *HTTPBackend) Name() string { return b.name }

// Read is not supported on generic HTTP backends.
func (b *HTTPBackend) Read(ctx context.Context, uri string) (*Resource, error) {
	return nil, fmt.Errorf("%w: http backend %q does not support Read", model.ErrResourceNotFound, b.name)
}

// ListResources is a noop — HTTP backends do not enumerate.
func (b *HTTPBackend) ListResources(ctx context.Context) ([]ResourceInfo, error) {
	return nil, nil
}

// Search fires a configured HTTP request and decodes the result array.
func (b *HTTPBackend) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	runCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	body := renderTemplate(b.search.BodyTemplate, map[string]string{
		"query": jsonEscape(query),
		"limit": fmt.Sprintf("%d", limit),
	})

	method := b.search.Method
	if method == "" {
		method = http.MethodPost
	}

	var reqBody io.Reader
	if body != "" {
		reqBody = bytes.NewBufferString(body)
	}
	req, err := http.NewRequestWithContext(runCtx, method, b.baseURL+b.search.Path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("http backend %q: build request: %w", b.name, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range b.search.Headers {
		req.Header.Set(k, v)
	}
	if b.authTok != "" {
		req.Header.Set("Authorization", "Bearer "+b.authTok)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: http backend %q timed out after %s",
				model.ErrBackendUnavailable, b.name, b.timeout)
		}
		return nil, fmt.Errorf("%w: http backend %q: %s", model.ErrBackendUnavailable, b.name, err.Error())
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("http backend %q: read response: %w", b.name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: http backend %q: http %d: %s",
			model.ErrBackendUnavailable, b.name, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	return decodeHTTPSearchResults(raw, b.search.ResponsePath, limit, b.name)
}

// decodeHTTPSearchResults walks raw JSON to the configured path and
// converts each array element into a SearchResult.
func decodeHTTPSearchResults(raw []byte, path string, limit int, backendName string) ([]SearchResult, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("http backend %q: decode response: %w", backendName, err)
	}
	node, ok := walkPath(decoded, path)
	if !ok {
		return nil, nil
	}
	arr, ok := node.([]any)
	if !ok {
		return nil, fmt.Errorf("http backend %q: response path %q is not an array", backendName, path)
	}

	out := make([]SearchResult, 0, min(len(arr), limit))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		uri, _ := m["uri"].(string)
		snippet, _ := m["snippet"].(string)
		var score *float64
		if s, ok := m["score"].(float64); ok {
			score = &s
		}
		meta := map[string]any{"backend": backendName}
		if mm, ok := m["metadata"].(map[string]any); ok {
			for k, v := range mm {
				if _, exists := meta[k]; !exists {
					meta[k] = v
				}
			}
		}
		out = append(out, SearchResult{
			URI:      uri,
			Snippet:  snippet,
			Score:    score,
			Metadata: meta,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
