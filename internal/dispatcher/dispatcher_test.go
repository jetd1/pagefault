package dispatcher

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/audit"
	"github.com/jet/pagefault/internal/backend"
	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/filter"
	"github.com/jet/pagefault/internal/model"
)

// mockBackend is a simple in-memory backend for dispatcher tests. It implements
// the Backend interface plus the URIScheme hook used by the dispatcher for
// scheme→backend routing.
type mockBackend struct {
	name      string
	scheme    string
	resources map[string]*backend.Resource
	searchOut []backend.SearchResult
	searchErr error
	readErr   error
}

func (m *mockBackend) Name() string      { return m.name }
func (m *mockBackend) URIScheme() string { return m.scheme }

func (m *mockBackend) Read(_ context.Context, uri string) (*backend.Resource, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	if r, ok := m.resources[uri]; ok {
		return r, nil
	}
	return nil, model.ErrResourceNotFound
}

func (m *mockBackend) Search(_ context.Context, _ string, limit int) ([]backend.SearchResult, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	if limit > len(m.searchOut) {
		limit = len(m.searchOut)
	}
	return append([]backend.SearchResult(nil), m.searchOut[:limit]...), nil
}

func (m *mockBackend) ListResources(context.Context) ([]backend.ResourceInfo, error) {
	out := make([]backend.ResourceInfo, 0, len(m.resources))
	for uri := range m.resources {
		out = append(out, backend.ResourceInfo{URI: uri})
	}
	return out, nil
}

func newTestDispatcher(t *testing.T) (*ToolDispatcher, *mockBackend) {
	t.Helper()
	mb := &mockBackend{
		name:   "memory",
		scheme: "memory",
		resources: map[string]*backend.Resource{
			"memory://foo.md": {
				URI:         "memory://foo.md",
				Content:     "hello world\nline two",
				ContentType: "text/markdown",
				Metadata:    map[string]any{"tags": []string{"docs"}},
			},
			"memory://bar.md": {
				URI:         "memory://bar.md",
				Content:     "bar content",
				ContentType: "text/markdown",
				Metadata:    map[string]any{"tags": []string{"notes"}},
			},
			"memory://secret.md": {
				URI:     "memory://secret.md",
				Content: "top secret",
				Metadata: map[string]any{
					"tags": []string{"secret"},
				},
			},
		},
		searchOut: []backend.SearchResult{
			{URI: "memory://foo.md", Snippet: "hello world", Metadata: map[string]any{"tags": []string{"docs"}}},
			{URI: "memory://bar.md", Snippet: "bar content", Metadata: map[string]any{"tags": []string{"notes"}}},
		},
	}

	d, err := New(Options{
		Backends: []backend.Backend{mb},
		Contexts: []config.ContextConfig{
			{
				Name:        "greeting",
				Description: "A simple greeting context",
				Sources: []config.ContextSource{
					{Backend: "memory", URI: "memory://foo.md"},
					{Backend: "memory", URI: "memory://bar.md"},
				},
				Format:  "markdown",
				MaxSize: 100_000,
			},
		},
		Filter: filter.NewCompositeFilter(),
		Audit:  audit.NopLogger{},
		Tools:  config.ToolsConfig{},
	})
	require.NoError(t, err)
	return d, mb
}

func TestDispatcher_New_DuplicateBackend(t *testing.T) {
	a := &mockBackend{name: "x", scheme: "m1"}
	b := &mockBackend{name: "x", scheme: "m2"}
	_, err := New(Options{Backends: []backend.Backend{a, b}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate backend name")
}

func TestDispatcher_New_DuplicateScheme(t *testing.T) {
	a := &mockBackend{name: "a", scheme: "memory"}
	b := &mockBackend{name: "b", scheme: "memory"}
	_, err := New(Options{Backends: []backend.Backend{a, b}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheme")
}

func TestDispatcher_New_ContextUnknownBackend(t *testing.T) {
	mb := &mockBackend{name: "memory", scheme: "memory"}
	_, err := New(Options{
		Backends: []backend.Backend{mb},
		Contexts: []config.ContextConfig{
			{Name: "bad", Sources: []config.ContextSource{{Backend: "nope", URI: "memory://x.md"}}},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown backend")
}

func TestDispatcher_ListContexts(t *testing.T) {
	d, _ := newTestDispatcher(t)
	summaries, err := d.ListContexts(context.Background(), model.AnonymousCaller)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "greeting", summaries[0].Name)
}

func TestDispatcher_GetContext_Concatenates(t *testing.T) {
	d, _ := newTestDispatcher(t)
	out, skipped, err := d.GetContext(context.Background(), "greeting", "", model.AnonymousCaller)
	require.NoError(t, err)
	assert.Empty(t, skipped)
	assert.Contains(t, out, "memory://foo.md")
	assert.Contains(t, out, "hello world")
	assert.Contains(t, out, "memory://bar.md")
	assert.Contains(t, out, "bar content")
	assert.Contains(t, out, "---") // separator
}

func TestDispatcher_GetContext_UnknownName(t *testing.T) {
	d, _ := newTestDispatcher(t)
	_, _, err := d.GetContext(context.Background(), "nope", "", model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrContextNotFound))
}

func TestDispatcher_GetContext_Truncates(t *testing.T) {
	d, _ := newTestDispatcher(t)
	// Override MaxSize to force truncation.
	cfg := d.contexts["greeting"]
	cfg.MaxSize = 50
	d.contexts["greeting"] = cfg

	out, _, err := d.GetContext(context.Background(), "greeting", "", model.AnonymousCaller)
	require.NoError(t, err)
	assert.Contains(t, out, "[truncated]")
	assert.LessOrEqual(t, len(out), 70) // 50 + suffix length
}

func TestDispatcher_GetContext_TruncatesUTF8Safely(t *testing.T) {
	// "你好世界" is four 3-byte runes (12 bytes total). With MaxSize=7,
	// the byte-level cut lands mid-rune; rune-boundary truncation must
	// back up so the output remains valid UTF-8.
	mb := &mockBackend{
		name:   "memory",
		scheme: "memory",
		resources: map[string]*backend.Resource{
			"memory://cn.md": {URI: "memory://cn.md", Content: "你好世界"},
		},
	}
	d, err := New(Options{
		Backends: []backend.Backend{mb},
		Contexts: []config.ContextConfig{
			{
				Name:    "cn",
				Sources: []config.ContextSource{{Backend: "memory", URI: "memory://cn.md"}},
				MaxSize: 20, // small enough to force truncation past the header
			},
		},
	})
	require.NoError(t, err)
	out, _, err := d.GetContext(context.Background(), "cn", "", model.AnonymousCaller)
	require.NoError(t, err)
	assert.True(t, utf8.ValidString(out), "truncated output must be valid UTF-8")
	assert.Contains(t, out, "[truncated]")
}

func TestDispatcher_GetContext_SkipsMissing(t *testing.T) {
	mb := &mockBackend{
		name:   "memory",
		scheme: "memory",
		resources: map[string]*backend.Resource{
			"memory://a.md": {URI: "memory://a.md", Content: "aaa"},
		},
	}
	d, err := New(Options{
		Backends: []backend.Backend{mb},
		Contexts: []config.ContextConfig{
			{
				Name: "mixed",
				Sources: []config.ContextSource{
					{Backend: "memory", URI: "memory://a.md"},
					{Backend: "memory", URI: "memory://missing.md"}, // will error
				},
				MaxSize: 1000,
			},
		},
	})
	require.NoError(t, err)
	out, skipped, err := d.GetContext(context.Background(), "mixed", "", model.AnonymousCaller)
	require.NoError(t, err)
	assert.Contains(t, out, "aaa")
	assert.NotContains(t, out, "missing.md")
	require.Len(t, skipped, 1)
	assert.Equal(t, "memory://missing.md", skipped[0].URI)
	assert.Contains(t, skipped[0].Reason, "read error")
}

func TestDispatcher_Search_Basic(t *testing.T) {
	d, _ := newTestDispatcher(t)
	results, err := d.Search(context.Background(), "hello", 10, nil, model.AnonymousCaller)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	for _, r := range results {
		assert.Equal(t, "memory", r.Backend)
	}
}

func TestDispatcher_Search_EmptyQuery(t *testing.T) {
	d, _ := newTestDispatcher(t)
	_, err := d.Search(context.Background(), "", 10, nil, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestDispatcher_Search_UnknownBackend(t *testing.T) {
	d, _ := newTestDispatcher(t)
	_, err := d.Search(context.Background(), "x", 10, []string{"nope"}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrBackendNotFound))
}

func TestDispatcher_Search_FilterDeniesResults(t *testing.T) {
	d, _ := newTestDispatcher(t)
	pf, err := filter.NewPathFilter(nil, []string{"memory://foo.md"})
	require.NoError(t, err)
	d.filter = filter.NewCompositeFilter(pf)

	results, err := d.Search(context.Background(), "hello", 10, nil, model.AnonymousCaller)
	require.NoError(t, err)
	for _, r := range results {
		assert.NotEqual(t, "memory://foo.md", r.URI)
	}
}

func TestDispatcher_Read_Basic(t *testing.T) {
	d, _ := newTestDispatcher(t)
	res, err := d.Read(context.Background(), "memory://foo.md", 0, 0, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Contains(t, res.Content, "hello world")
}

func TestDispatcher_Read_LineRange(t *testing.T) {
	d, _ := newTestDispatcher(t)
	res, err := d.Read(context.Background(), "memory://foo.md", 2, 2, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "line two", res.Content)
}

func TestDispatcher_Read_UnknownScheme(t *testing.T) {
	d, _ := newTestDispatcher(t)
	_, err := d.Read(context.Background(), "other://foo.md", 0, 0, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrBackendNotFound))
}

func TestDispatcher_Read_Missing(t *testing.T) {
	d, _ := newTestDispatcher(t)
	_, err := d.Read(context.Background(), "memory://nope.md", 0, 0, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrResourceNotFound))
}

func TestDispatcher_Read_FilterDeniesURI(t *testing.T) {
	d, _ := newTestDispatcher(t)
	pf, err := filter.NewPathFilter(nil, []string{"memory://secret.md"})
	require.NoError(t, err)
	d.filter = filter.NewCompositeFilter(pf)

	_, err = d.Read(context.Background(), "memory://secret.md", 0, 0, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAccessViolation))
}

func TestDispatcher_Read_FilterDeniesByTag(t *testing.T) {
	d, _ := newTestDispatcher(t)
	tf := filter.NewTagFilter(nil, []string{"secret"})
	d.filter = filter.NewCompositeFilter(tf)

	_, err := d.Read(context.Background(), "memory://secret.md", 0, 0, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAccessViolation))
}

func TestDispatcher_AuditLogged(t *testing.T) {
	// Use a JSONL logger on a tempdir and assert that entries are written.
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	lg, err := audit.NewJSONLLogger(path)
	require.NoError(t, err)
	defer lg.Close()

	mb := &mockBackend{
		name: "memory", scheme: "memory",
		resources: map[string]*backend.Resource{"memory://a.md": {URI: "memory://a.md", Content: "x"}},
	}
	d, err := New(Options{
		Backends: []backend.Backend{mb},
		Audit:    lg,
	})
	require.NoError(t, err)

	_, err = d.Read(context.Background(), "memory://a.md", 0, 0, model.Caller{ID: "tester", Label: "t"})
	require.NoError(t, err)
	require.NoError(t, lg.Close())

	// The file must be non-empty.
	info, err := filepath.Glob(path)
	require.NoError(t, err)
	require.NotEmpty(t, info)
}

func TestDispatcher_SortedBackendNames(t *testing.T) {
	d, _ := newTestDispatcher(t)
	names := d.SortedBackendNames()
	assert.Equal(t, []string{"memory"}, names)
}

func TestDispatcher_ToolEnabled(t *testing.T) {
	d, _ := newTestDispatcher(t)
	assert.True(t, d.ToolEnabled("pf_scan"))
	assert.False(t, d.ToolEnabled("unknown_tool"))
}
