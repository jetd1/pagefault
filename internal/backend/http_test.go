package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

func TestNewHTTPBackend_Validation(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.HTTPBackendConfig
		err  bool
	}{
		{"nil", nil, true},
		{"missing-base-url", &config.HTTPBackendConfig{Name: "x", Type: "http", Search: config.HTTPBackendRequest{Path: "/s"}}, true},
		{"missing-search-path", &config.HTTPBackendConfig{Name: "x", Type: "http", BaseURL: "http://x"}, true},
		{"ok", &config.HTTPBackendConfig{
			Name: "x", Type: "http", BaseURL: "http://x",
			Search: config.HTTPBackendRequest{Path: "/s"},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewHTTPBackend(tt.cfg)
			if tt.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestHTTPBackend_Search_HappyPath(t *testing.T) {
	var gotBody string
	var gotAuth string
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
		  "results": [
		    {"uri": "lcm://a", "snippet": "first", "score": 0.9, "metadata": {"tags": ["x"]}},
		    {"uri": "lcm://b", "snippet": "second"}
		  ]
		}`)
	}))
	defer srv.Close()

	b, err := NewHTTPBackend(&config.HTTPBackendConfig{
		Name: "lcm", Type: "http", BaseURL: srv.URL,
		Auth: config.HTTPBackendAuth{Mode: "bearer", Token: "tok"},
		Search: config.HTTPBackendRequest{
			Method:       "POST",
			Path:         "/api/search",
			BodyTemplate: `{"query": "{query}", "limit": {limit}}`,
			ResponsePath: "results",
		},
		Timeout: 10,
	})
	require.NoError(t, err)

	out, err := b.Search(context.Background(), `say "hi"`, 5)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "lcm://a", out[0].URI)
	assert.Equal(t, "first", out[0].Snippet)
	require.NotNil(t, out[0].Score)
	assert.Equal(t, 0.9, *out[0].Score)
	assert.Equal(t, "lcm", out[0].Metadata["backend"])
	// Metadata from server is merged in.
	if tags, ok := out[0].Metadata["tags"].([]any); ok {
		assert.Equal(t, "x", tags[0])
	}
	assert.Equal(t, "lcm://b", out[1].URI)

	assert.Equal(t, "Bearer tok", gotAuth)
	assert.Equal(t, "application/json", gotAccept)
	assert.Contains(t, gotBody, `\"hi\"`)
	assert.Contains(t, gotBody, `"limit": 5`)
}

func TestHTTPBackend_Search_Limit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[
		  {"uri":"a"},{"uri":"b"},{"uri":"c"},{"uri":"d"}
		]}`)
	}))
	defer srv.Close()

	b, err := NewHTTPBackend(&config.HTTPBackendConfig{
		Name: "x", Type: "http", BaseURL: srv.URL,
		Search: config.HTTPBackendRequest{Path: "/s", ResponsePath: "results"},
	})
	require.NoError(t, err)

	out, err := b.Search(context.Background(), "q", 2)
	require.NoError(t, err)
	assert.Len(t, out, 2)
}

func TestHTTPBackend_Search_ResponsePathMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"other": []}`)
	}))
	defer srv.Close()

	b, err := NewHTTPBackend(&config.HTTPBackendConfig{
		Name: "x", Type: "http", BaseURL: srv.URL,
		Search: config.HTTPBackendRequest{Path: "/s", ResponsePath: "results"},
	})
	require.NoError(t, err)

	out, err := b.Search(context.Background(), "q", 10)
	require.NoError(t, err)
	assert.Nil(t, out)
}

func TestHTTPBackend_Search_NonArrayPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results": "not an array"}`)
	}))
	defer srv.Close()

	b, err := NewHTTPBackend(&config.HTTPBackendConfig{
		Name: "x", Type: "http", BaseURL: srv.URL,
		Search: config.HTTPBackendRequest{Path: "/s", ResponsePath: "results"},
	})
	require.NoError(t, err)

	_, err = b.Search(context.Background(), "q", 10)
	require.Error(t, err)
}

func TestHTTPBackend_Search_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "down")
	}))
	defer srv.Close()

	b, err := NewHTTPBackend(&config.HTTPBackendConfig{
		Name: "x", Type: "http", BaseURL: srv.URL,
		Search: config.HTTPBackendRequest{Path: "/s", ResponsePath: "results"},
	})
	require.NoError(t, err)

	_, err = b.Search(context.Background(), "q", 10)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrBackendUnavailable))
}

func TestHTTPBackend_Search_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b, err := NewHTTPBackend(&config.HTTPBackendConfig{
		Name: "x", Type: "http", BaseURL: srv.URL,
		Search:  config.HTTPBackendRequest{Path: "/s"},
		Timeout: 1, // seconds; further clamped below
	})
	require.NoError(t, err)
	// Override to a tight deadline via internal timeout — we can't
	// inject time.Duration directly, so assert it finishes within a
	// reasonable window (< ~3s) and returns an error.
	b.timeout = 100 * time.Millisecond

	start := time.Now()
	_, err = b.Search(context.Background(), "q", 10)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrBackendUnavailable))
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestHTTPBackend_Read_Unsupported(t *testing.T) {
	b, err := NewHTTPBackend(&config.HTTPBackendConfig{
		Name: "x", Type: "http", BaseURL: "http://x",
		Search: config.HTTPBackendRequest{Path: "/s"},
	})
	require.NoError(t, err)

	_, err = b.Read(context.Background(), "x://y")
	assert.True(t, errors.Is(err, model.ErrResourceNotFound))

	list, err := b.ListResources(context.Background())
	require.NoError(t, err)
	assert.Nil(t, list)
}

func TestHTTPBackend_Search_EmptyQuery(t *testing.T) {
	b, err := NewHTTPBackend(&config.HTTPBackendConfig{
		Name: "x", Type: "http", BaseURL: "http://x",
		Search: config.HTTPBackendRequest{Path: "/s"},
	})
	require.NoError(t, err)

	out, err := b.Search(context.Background(), "", 10)
	require.NoError(t, err)
	assert.Nil(t, out)
}
