package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

func TestNewSubagentHTTPBackend_Validation(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.SubagentHTTPBackendConfig
		err  bool
	}{
		{"nil", nil, true},
		{"missing-base-url", &config.SubagentHTTPBackendConfig{
			Name: "x", Type: "subagent-http",
			Spawn:  config.HTTPBackendRequest{Path: "/spawn"},
			Agents: []config.AgentSpec{{ID: "a"}},
		}, true},
		{"missing-spawn-path", &config.SubagentHTTPBackendConfig{
			Name: "x", Type: "subagent-http", BaseURL: "http://x",
			Agents: []config.AgentSpec{{ID: "a"}},
		}, true},
		{"no-agents", &config.SubagentHTTPBackendConfig{
			Name: "x", Type: "subagent-http", BaseURL: "http://x",
			Spawn: config.HTTPBackendRequest{Path: "/spawn"},
		}, true},
		{"ok", &config.SubagentHTTPBackendConfig{
			Name: "x", Type: "subagent-http", BaseURL: "http://x",
			Spawn:  config.HTTPBackendRequest{Path: "/spawn"},
			Agents: []config.AgentSpec{{ID: "a"}},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSubagentHTTPBackend(tt.cfg)
			if tt.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSubagentHTTPBackend_Spawn_HappyPath(t *testing.T) {
	var gotBody string
	var gotAuth string
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotURL = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result": {"answer": "42"}}`)
	}))
	defer srv.Close()

	b, err := NewSubagentHTTPBackend(&config.SubagentHTTPBackendConfig{
		Name:    "sa",
		Type:    "subagent-http",
		BaseURL: srv.URL,
		Auth:    config.HTTPBackendAuth{Mode: "bearer", Token: "secret"},
		Spawn: config.HTTPBackendRequest{
			Method:       "POST",
			Path:         "/agents/{agent_id}/run",
			BodyTemplate: `{"task": "{task}", "timeout": {timeout}}`,
			ResponsePath: "result.answer",
		},
		Timeout:                10,
		Agents:                 []config.AgentSpec{{ID: "wocha", Description: "dev"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	out, err := b.Spawn(context.Background(), SpawnRequest{
		AgentID: "wocha",
		Task:    `hello "quoted" world`,
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, "42", out)
	assert.Equal(t, "Bearer secret", gotAuth)
	assert.Equal(t, "/agents/wocha/run", gotURL)
	// Task is JSON-escaped inside the body template so the result parses.
	assert.Contains(t, gotBody, `hello \"quoted\" world`)
	assert.Contains(t, gotBody, `"timeout": 5`)
}

func TestSubagentHTTPBackend_Spawn_ResponsePathMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"other": "field"}`)
	}))
	defer srv.Close()

	b, err := NewSubagentHTTPBackend(&config.SubagentHTTPBackendConfig{
		Name: "sa", Type: "subagent-http", BaseURL: srv.URL,
		Spawn:                  config.HTTPBackendRequest{Path: "/spawn", ResponsePath: "result.answer"},
		Agents:                 []config.AgentSpec{{ID: "a"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	_, err = b.Spawn(context.Background(), SpawnRequest{AgentID: "a", Task: "t", Timeout: 5 * time.Second})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSubagentHTTPBackend_Spawn_EmptyResponsePathReturnsRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `just text`)
	}))
	defer srv.Close()

	b, err := NewSubagentHTTPBackend(&config.SubagentHTTPBackendConfig{
		Name: "sa", Type: "subagent-http", BaseURL: srv.URL,
		Spawn:                  config.HTTPBackendRequest{Path: "/spawn"},
		Agents:                 []config.AgentSpec{{ID: "a"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	out, err := b.Spawn(context.Background(), SpawnRequest{AgentID: "a", Task: "t", Timeout: 5 * time.Second})
	require.NoError(t, err)
	assert.Equal(t, "just text", out)
}

func TestSubagentHTTPBackend_Spawn_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer srv.Close()

	b, err := NewSubagentHTTPBackend(&config.SubagentHTTPBackendConfig{
		Name: "sa", Type: "subagent-http", BaseURL: srv.URL,
		Spawn:                  config.HTTPBackendRequest{Path: "/spawn", ResponsePath: "x"},
		Agents:                 []config.AgentSpec{{ID: "a"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	_, err = b.Spawn(context.Background(), SpawnRequest{AgentID: "a", Task: "t", Timeout: 5 * time.Second})
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrBackendUnavailable))
	assert.Contains(t, err.Error(), "http 500")
}

func TestSubagentHTTPBackend_Spawn_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b, err := NewSubagentHTTPBackend(&config.SubagentHTTPBackendConfig{
		Name: "sa", Type: "subagent-http", BaseURL: srv.URL,
		Spawn:                  config.HTTPBackendRequest{Path: "/spawn"},
		Agents:                 []config.AgentSpec{{ID: "a"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	_, err = b.Spawn(context.Background(), SpawnRequest{AgentID: "a", Task: "t", Timeout: 50 * time.Millisecond})
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrSubagentTimeout), "got: %v", err)
}

func TestSubagentHTTPBackend_Spawn_UnknownAgent(t *testing.T) {
	b, err := NewSubagentHTTPBackend(&config.SubagentHTTPBackendConfig{
		Name: "sa", Type: "subagent-http", BaseURL: "http://x",
		Spawn:                  config.HTTPBackendRequest{Path: "/spawn"},
		Agents:                 []config.AgentSpec{{ID: "a"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	_, err = b.Spawn(context.Background(), SpawnRequest{AgentID: "nope", Task: "t", Timeout: 5 * time.Second})
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAgentNotFound))
}

func TestSubagentHTTPBackend_NoopReadSearchList(t *testing.T) {
	b, err := NewSubagentHTTPBackend(&config.SubagentHTTPBackendConfig{
		Name: "sa", Type: "subagent-http", BaseURL: "http://x",
		Spawn:  config.HTTPBackendRequest{Path: "/spawn"},
		Agents: []config.AgentSpec{{ID: "a", Description: "d"}},
	})
	require.NoError(t, err)

	_, err = b.Read(context.Background(), "x://y")
	assert.True(t, errors.Is(err, model.ErrResourceNotFound))

	res, err := b.Search(context.Background(), "q", 10)
	require.NoError(t, err)
	assert.Nil(t, res)

	list, err := b.ListResources(context.Background())
	require.NoError(t, err)
	assert.Nil(t, list)

	agents := b.ListAgents()
	require.Len(t, agents, 1)
	assert.Equal(t, "a", agents[0].ID)
	assert.Equal(t, "d", agents[0].Description)
	assert.Equal(t, "a", b.DefaultAgentID())
}

// Helper tests for renderTemplate / jsonEscape / walkPath /
// extractResponse live in http_helpers_test.go so both the subagent
// and generic HTTP backends can share them cleanly.

// TestSubagentHTTPBackend_Spawn_SpawnIDPassthrough — the 0.10.0
// {spawn_id} placeholder is substituted into BOTH the URL path and
// the body template, so operators can route a per-call unique
// session key to the downstream HTTP gateway any way it wants.
func TestSubagentHTTPBackend_Spawn_SpawnIDPassthrough(t *testing.T) {
	var gotURL, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = io.WriteString(w, `"ok"`)
	}))
	defer srv.Close()

	b, err := NewSubagentHTTPBackend(&config.SubagentHTTPBackendConfig{
		Name: "sa", Type: "subagent-http", BaseURL: srv.URL,
		Spawn: config.HTTPBackendRequest{
			Method:       "POST",
			Path:         "/sessions/{spawn_id}/spawn",
			BodyTemplate: `{"session_id":"{spawn_id}","task":"{task}"}`,
		},
		Agents:                 []config.AgentSpec{{ID: "a"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	_, err = b.Spawn(context.Background(), SpawnRequest{
		AgentID: "a",
		Task:    "q",
		SpawnID: "pf_sp_fixture",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, "/sessions/pf_sp_fixture/spawn", gotURL)
	assert.Contains(t, gotBody, `"session_id":"pf_sp_fixture"`)
}

// ensure we send json content type only when a body is present
func TestSubagentHTTPBackend_Spawn_NoBodyNoContentType(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		_, _ = fmt.Fprint(w, `"ok"`)
	}))
	defer srv.Close()

	b, err := NewSubagentHTTPBackend(&config.SubagentHTTPBackendConfig{
		Name: "sa", Type: "subagent-http", BaseURL: srv.URL,
		Spawn:                  config.HTTPBackendRequest{Path: "/spawn"},
		Agents:                 []config.AgentSpec{{ID: "a"}},
		RetrievePromptTemplate: passthroughTmpl,
	})
	require.NoError(t, err)

	out, err := b.Spawn(context.Background(), SpawnRequest{AgentID: "a", Task: "t", Timeout: 5 * time.Second})
	require.NoError(t, err)
	assert.Equal(t, `"ok"`, strings.TrimSpace(out))
	assert.Empty(t, gotCT)
}
