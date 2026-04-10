package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

// SubagentHTTPBackend spawns an agent by POSTing to a configured HTTP
// endpoint and waits synchronously for the response. It satisfies the
// SubagentBackend interface for pf_fault.
//
// The request body is built from cfg.Spawn.BodyTemplate with {agent_id},
// {task}, and {timeout} substituted at spawn time. The response must be
// JSON; cfg.Spawn.ResponsePath is a simple dotted path (e.g. "result" or
// "data.answer") that extracts the final text from the body. If empty,
// the entire response is JSON-re-encoded and returned.
type SubagentHTTPBackend struct {
	name    string
	baseURL string
	authTok string
	spawn   config.HTTPBackendRequest
	timeout time.Duration
	agents  []AgentInfo
	defID   string
	client  *http.Client
}

// NewSubagentHTTPBackend constructs an HTTP subagent backend from config.
func NewSubagentHTTPBackend(cfg *config.SubagentHTTPBackendConfig) (*SubagentHTTPBackend, error) {
	if cfg == nil {
		return nil, errors.New("subagent-http backend: nil config")
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("subagent-http backend %q: base_url is required", cfg.Name)
	}
	if cfg.Spawn.Path == "" {
		return nil, fmt.Errorf("subagent-http backend %q: spawn.path is required", cfg.Name)
	}
	if len(cfg.Agents) == 0 {
		return nil, fmt.Errorf("subagent-http backend %q: no agents configured", cfg.Name)
	}

	agents := make([]AgentInfo, 0, len(cfg.Agents))
	seen := make(map[string]bool, len(cfg.Agents))
	for _, a := range cfg.Agents {
		if a.ID == "" {
			return nil, fmt.Errorf("subagent-http backend %q: agent with empty id", cfg.Name)
		}
		if seen[a.ID] {
			return nil, fmt.Errorf("subagent-http backend %q: duplicate agent id %q", cfg.Name, a.ID)
		}
		seen[a.ID] = true
		agents = append(agents, AgentInfo{ID: a.ID, Description: a.Description})
	}

	timeoutSec := cfg.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 300
	}
	authTok := ""
	if cfg.Auth.Mode == "bearer" {
		authTok = cfg.Auth.Token
	}

	return &SubagentHTTPBackend{
		name:    cfg.Name,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		authTok: authTok,
		spawn:   cfg.Spawn,
		timeout: time.Duration(timeoutSec) * time.Second,
		agents:  agents,
		defID:   agents[0].ID,
		// No per-request client timeout: we rely on the per-call context
		// deadline set in Spawn so timeouts can be overridden per call.
		client: &http.Client{},
	}, nil
}

// Name returns the backend name.
func (b *SubagentHTTPBackend) Name() string { return b.name }

// Read is a noop for subagent backends.
func (b *SubagentHTTPBackend) Read(ctx context.Context, uri string) (*Resource, error) {
	return nil, fmt.Errorf("%w: subagent backend %q cannot Read URIs (use pf_fault)",
		model.ErrResourceNotFound, b.name)
}

// Search is a noop for subagent backends.
func (b *SubagentHTTPBackend) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	return nil, nil
}

// ListResources is a noop for subagent backends.
func (b *SubagentHTTPBackend) ListResources(ctx context.Context) ([]ResourceInfo, error) {
	return nil, nil
}

// ListAgents returns the configured agents.
func (b *SubagentHTTPBackend) ListAgents() []AgentInfo {
	out := make([]AgentInfo, len(b.agents))
	copy(out, b.agents)
	return out
}

// DefaultAgentID returns the default agent id for empty-Spawn calls.
func (b *SubagentHTTPBackend) DefaultAgentID() string { return b.defID }

// Spawn POSTs the configured request body to the agent endpoint and
// returns the extracted response text.
func (b *SubagentHTTPBackend) Spawn(ctx context.Context, agentID string, task string, timeout time.Duration) (string, error) {
	if agentID == "" {
		agentID = b.defID
	}
	if !hasAgentID(b.agents, agentID) {
		return "", fmt.Errorf("%w: %q on backend %q", model.ErrAgentNotFound, agentID, b.name)
	}
	if timeout <= 0 {
		timeout = b.timeout
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Render the URL with the same placeholders — many HTTP agent APIs
	// put the agent id in the path.
	urlPath := strings.ReplaceAll(b.spawn.Path, "{agent_id}", agentID)
	url := b.baseURL + urlPath

	body := renderTemplate(b.spawn.BodyTemplate, map[string]string{
		"agent_id": agentID,
		"task":     jsonEscape(task),
		"timeout":  fmt.Sprintf("%d", int(timeout.Seconds())),
	})

	method := b.spawn.Method
	if method == "" {
		method = http.MethodPost
	}

	var reqBody io.Reader
	if body != "" {
		reqBody = bytes.NewBufferString(body)
	}
	req, err := http.NewRequestWithContext(runCtx, method, url, reqBody)
	if err != nil {
		return "", fmt.Errorf("subagent-http %q: build request: %w", b.name, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range b.spawn.Headers {
		req.Header.Set(k, v)
	}
	if b.authTok != "" {
		req.Header.Set("Authorization", "Bearer "+b.authTok)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%w: agent %q on backend %q timed out after %s",
				model.ErrSubagentTimeout, agentID, b.name, timeout)
		}
		return "", fmt.Errorf("%w: subagent-http %q: %s", model.ErrBackendUnavailable, b.name, err.Error())
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("subagent-http %q: read response: %w", b.name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: subagent-http %q: http %d: %s",
			model.ErrBackendUnavailable, b.name, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	return extractResponse(raw, b.spawn.ResponsePath)
}

// Shared HTTP template / JSON-path helpers (renderTemplate, jsonEscape,
// walkPath, extractResponse) live in http_helpers.go — both this file
// and http.go depend on them.
