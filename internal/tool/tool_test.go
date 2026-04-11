package tool

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"jetd.one/pagefault/internal/audit"
	"jetd.one/pagefault/internal/backend"
	"jetd.one/pagefault/internal/config"
	"jetd.one/pagefault/internal/dispatcher"
	"jetd.one/pagefault/internal/filter"
	"jetd.one/pagefault/internal/model"
)

// fakeBackend is a minimal backend for tool-layer tests.
type fakeBackend struct {
	resources map[string]*backend.Resource
	search    []backend.SearchResult
}

func (*fakeBackend) Name() string      { return "fake" }
func (*fakeBackend) URIScheme() string { return "memory" }

func (f *fakeBackend) Read(_ context.Context, uri string) (*backend.Resource, error) {
	if r, ok := f.resources[uri]; ok {
		return r, nil
	}
	return nil, model.ErrResourceNotFound
}

func (f *fakeBackend) Search(_ context.Context, _ string, limit int) ([]backend.SearchResult, error) {
	if limit > len(f.search) {
		limit = len(f.search)
	}
	return append([]backend.SearchResult(nil), f.search[:limit]...), nil
}

func (f *fakeBackend) ListResources(context.Context) ([]backend.ResourceInfo, error) {
	out := make([]backend.ResourceInfo, 0, len(f.resources))
	for uri := range f.resources {
		out = append(out, backend.ResourceInfo{URI: uri})
	}
	return out, nil
}

func makeDispatcher(t *testing.T) *dispatcher.ToolDispatcher {
	t.Helper()
	fb := &fakeBackend{
		resources: map[string]*backend.Resource{
			"memory://foo.md": {
				URI:     "memory://foo.md",
				Content: "line1\nline2\nline3",
			},
		},
		search: []backend.SearchResult{
			{URI: "memory://foo.md", Snippet: "line1"},
		},
	}
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{fb},
		Contexts: []config.ContextConfig{
			{
				Name:        "demo",
				Description: "A demo context",
				Sources:     []config.ContextSource{{Backend: "fake", URI: "memory://foo.md"}},
				MaxSize:     10_000,
			},
		},
		Filter: filter.NewCompositeFilter(),
		Audit:  audit.NopLogger{},
	})
	require.NoError(t, err)
	return d
}

func TestHandleListContexts(t *testing.T) {
	d := makeDispatcher(t)
	out, err := HandleListContexts(context.Background(), d, ListContextsInput{}, model.AnonymousCaller)
	require.NoError(t, err)
	require.Len(t, out.Contexts, 1)
	assert.Equal(t, "demo", out.Contexts[0].Name)
	assert.Equal(t, "A demo context", out.Contexts[0].Description)
}

func TestHandleGetContext_Success(t *testing.T) {
	d := makeDispatcher(t)
	out, err := HandleGetContext(context.Background(), d, GetContextInput{Name: "demo"}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "demo", out.Name)
	assert.Equal(t, "markdown", out.Format)
	assert.Contains(t, out.Content, "line1")
}

func TestHandleGetContext_DefaultFormat(t *testing.T) {
	d := makeDispatcher(t)
	out, err := HandleGetContext(context.Background(), d, GetContextInput{Name: "demo", Format: ""}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "markdown", out.Format)
}

// TestHandleGetContext_EchoesResolvedFormat is a regression test for a
// bug where HandleGetContext echoed the caller-supplied format back
// verbatim, defaulting to "markdown" when empty. If the context's
// configured default was "json" or "markdown-with-metadata", the
// handler would return JSON content while reporting `format:"markdown"`.
// Fix: the dispatcher returns the resolved format and the handler
// echoes that.
func TestHandleGetContext_EchoesResolvedFormat(t *testing.T) {
	fb := &fakeBackend{
		resources: map[string]*backend.Resource{
			"memory://foo.md": {URI: "memory://foo.md", Content: "hi", ContentType: "text/markdown"},
		},
	}
	d, err := dispatcher.New(dispatcher.Options{
		Backends: []backend.Backend{fb},
		Contexts: []config.ContextConfig{
			{
				Name:    "jsonctx",
				Sources: []config.ContextSource{{Backend: "fake", URI: "memory://foo.md"}},
				Format:  "json", // context default is JSON
				MaxSize: 10_000,
			},
		},
		Filter: filter.NewCompositeFilter(),
		Audit:  audit.NopLogger{},
	})
	require.NoError(t, err)

	// Caller leaves in.Format empty → dispatcher falls through to cfg.Format
	// → "json". Handler must echo "json", not the old "markdown" fallback.
	out, err := HandleGetContext(context.Background(), d,
		GetContextInput{Name: "jsonctx", Format: ""}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "json", out.Format)
	assert.Contains(t, out.Content, `"name":"jsonctx"`)
}

func TestHandleGetContext_MissingName(t *testing.T) {
	d := makeDispatcher(t)
	_, err := HandleGetContext(context.Background(), d, GetContextInput{}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestHandleGetContext_UnknownName(t *testing.T) {
	d := makeDispatcher(t)
	_, err := HandleGetContext(context.Background(), d, GetContextInput{Name: "nope"}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrContextNotFound))
}

func TestHandleSearch_Success(t *testing.T) {
	d := makeDispatcher(t)
	out, err := HandleSearch(context.Background(), d, SearchInput{Query: "x", Limit: 5}, model.AnonymousCaller)
	require.NoError(t, err)
	require.Len(t, out.Results, 1)
	assert.Equal(t, "memory://foo.md", out.Results[0].URI)
	assert.Equal(t, "fake", out.Results[0].Backend)
}

func TestHandleSearch_EmptyQuery(t *testing.T) {
	d := makeDispatcher(t)
	_, err := HandleSearch(context.Background(), d, SearchInput{Query: ""}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestHandleSearch_DefaultLimit(t *testing.T) {
	// Limit defaults to 10 when zero.
	d := makeDispatcher(t)
	out, err := HandleSearch(context.Background(), d, SearchInput{Query: "x"}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(out.Results), 10)
}

func TestHandleRead_Success(t *testing.T) {
	d := makeDispatcher(t)
	out, err := HandleRead(context.Background(), d, ReadInput{URI: "memory://foo.md"}, model.AnonymousCaller)
	require.NoError(t, err)
	require.NotNil(t, out.Resource)
	assert.Equal(t, "memory://foo.md", out.Resource.URI)
	assert.Contains(t, out.Resource.Content, "line1")
}

func TestHandleRead_LineRange(t *testing.T) {
	d := makeDispatcher(t)
	out, err := HandleRead(context.Background(), d, ReadInput{URI: "memory://foo.md", FromLine: 2, ToLine: 2}, model.AnonymousCaller)
	require.NoError(t, err)
	assert.Equal(t, "line2", out.Resource.Content)
}

func TestHandleRead_MissingURI(t *testing.T) {
	d := makeDispatcher(t)
	_, err := HandleRead(context.Background(), d, ReadInput{}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestHandleRead_UnknownURI(t *testing.T) {
	d := makeDispatcher(t)
	_, err := HandleRead(context.Background(), d, ReadInput{URI: "memory://nope.md"}, model.AnonymousCaller)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrResourceNotFound))
}

func TestAsInt_Variants(t *testing.T) {
	assert.Equal(t, 0, asInt(nil))
	assert.Equal(t, 5, asInt(5))
	assert.Equal(t, 5, asInt(int64(5)))
	assert.Equal(t, 5, asInt(float64(5)))
	assert.Equal(t, 0, asInt("not-a-number"))
}

func TestAsString_Variants(t *testing.T) {
	assert.Equal(t, "", asString(nil))
	assert.Equal(t, "x", asString("x"))
	assert.Equal(t, "5", asString(5))
}

func TestAsStringSlice_Variants(t *testing.T) {
	assert.Nil(t, asStringSlice(nil))
	assert.Equal(t, []string{"a", "b"}, asStringSlice([]string{"a", "b"}))
	assert.Equal(t, []string{"a", "b"}, asStringSlice([]any{"a", "b"}))
	// Mixed slice drops non-strings.
	assert.Equal(t, []string{"a"}, asStringSlice([]any{"a", 1}))
}
