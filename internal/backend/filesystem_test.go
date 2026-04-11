package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"jetd.one/pagefault/internal/config"
	"jetd.one/pagefault/internal/model"
)

// newTestBackend constructs a backend rooted at testdata/sample.
func newTestBackend(t *testing.T) *FilesystemBackend {
	t.Helper()
	root, err := filepath.Abs("testdata/sample")
	require.NoError(t, err)

	cfg := &config.FilesystemBackendConfig{
		Name:      "fs",
		Type:      "filesystem",
		Root:      root,
		Include:   []string{"**/*.md"},
		Exclude:   []string{"notes/private.md"},
		URIScheme: "memory",
		AutoTag: map[string][]string{
			"notes/**/*.md": {"daily", "notes"},
			"README.md":     {"docs", "root"},
		},
		Sandbox: true,
	}
	be, err := NewFilesystemBackend(cfg)
	require.NoError(t, err)
	return be
}

func TestFilesystem_Read_Basic(t *testing.T) {
	be := newTestBackend(t)
	ctx := context.Background()

	res, err := be.Read(ctx, "memory://README.md")
	require.NoError(t, err)
	assert.Equal(t, "memory://README.md", res.URI)
	assert.Contains(t, res.Content, "Sample README")
	assert.Equal(t, "text/markdown", res.ContentType)
	assert.Equal(t, "fs", res.Metadata["backend"])
	assert.NotNil(t, res.Metadata["tags"])
	tags := res.Metadata["tags"].([]string)
	assert.Contains(t, tags, "docs")
	assert.Contains(t, tags, "root")
}

func TestFilesystem_Read_Nested(t *testing.T) {
	be := newTestBackend(t)
	res, err := be.Read(context.Background(), "memory://notes/daily.md")
	require.NoError(t, err)
	assert.Contains(t, res.Content, "Daily Notes")
	tags := res.Metadata["tags"].([]string)
	assert.Contains(t, tags, "daily")
	assert.Contains(t, tags, "notes")
}

func TestFilesystem_Read_Excluded(t *testing.T) {
	be := newTestBackend(t)
	_, err := be.Read(context.Background(), "memory://notes/private.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrResourceNotFound))
}

func TestFilesystem_Read_NotMatchingInclude(t *testing.T) {
	be := newTestBackend(t)
	_, err := be.Read(context.Background(), "memory://skipme.txt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrResourceNotFound))
}

func TestFilesystem_Read_NotExist(t *testing.T) {
	be := newTestBackend(t)
	_, err := be.Read(context.Background(), "memory://nonexistent.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrResourceNotFound))
}

func TestFilesystem_Read_WrongScheme(t *testing.T) {
	be := newTestBackend(t)
	_, err := be.Read(context.Background(), "other://README.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestFilesystem_Read_PathTraversal(t *testing.T) {
	be := newTestBackend(t)
	_, err := be.Read(context.Background(), "memory://../../../etc/passwd")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAccessViolation))
}

func TestFilesystem_Read_AbsolutePathInURI(t *testing.T) {
	be := newTestBackend(t)
	_, err := be.Read(context.Background(), "memory:///etc/passwd")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestFilesystem_Search_FindsMatch(t *testing.T) {
	be := newTestBackend(t)
	results, err := be.Search(context.Background(), "pagefault", 10)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// Should find matches in at least the README and daily.md.
	var uris []string
	for _, r := range results {
		uris = append(uris, r.URI)
		assert.NotEmpty(t, r.Snippet)
		assert.Equal(t, "fs", r.Metadata["backend"])
		assert.NotNil(t, r.Metadata["line"])
	}
	assert.Contains(t, uris, "memory://README.md")
}

func TestFilesystem_Search_CaseInsensitive(t *testing.T) {
	be := newTestBackend(t)
	results, err := be.Search(context.Background(), "PAGEFAULT", 10)
	require.NoError(t, err)
	require.NotEmpty(t, results)
}

func TestFilesystem_Search_NoResults(t *testing.T) {
	be := newTestBackend(t)
	results, err := be.Search(context.Background(), "xyz-no-such-word-expected", 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestFilesystem_Search_RespectsLimit(t *testing.T) {
	be := newTestBackend(t)
	results, err := be.Search(context.Background(), "e", 2)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(results), 2)
}

func TestFilesystem_Search_EmptyQuery(t *testing.T) {
	be := newTestBackend(t)
	_, err := be.Search(context.Background(), "", 10)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestFilesystem_Search_SkipsExcluded(t *testing.T) {
	be := newTestBackend(t)
	// "excluded" appears only in the private.md file; should not be found.
	results, err := be.Search(context.Background(), "should be excluded", 10)
	require.NoError(t, err)
	for _, r := range results {
		assert.NotEqual(t, "memory://notes/private.md", r.URI)
	}
}

func TestFilesystem_ListResources(t *testing.T) {
	be := newTestBackend(t)
	res, err := be.ListResources(context.Background())
	require.NoError(t, err)

	var uris []string
	for _, r := range res {
		uris = append(uris, r.URI)
	}
	assert.Contains(t, uris, "memory://README.md")
	assert.Contains(t, uris, "memory://notes/daily.md")
	assert.NotContains(t, uris, "memory://notes/private.md")
	assert.NotContains(t, uris, "memory://skipme.txt")
}

func TestFilesystem_NewBackend_MissingRoot(t *testing.T) {
	cfg := &config.FilesystemBackendConfig{
		Name:      "fs",
		Type:      "filesystem",
		Root:      "/nonexistent/path/xyz-12345",
		URIScheme: "memory",
		Sandbox:   true,
	}
	_, err := NewFilesystemBackend(cfg)
	require.Error(t, err)
}

func TestFilesystem_NewBackend_RootIsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	cfg := &config.FilesystemBackendConfig{
		Name:      "fs",
		Type:      "filesystem",
		Root:      file,
		URIScheme: "memory",
		Sandbox:   true,
	}
	_, err := NewFilesystemBackend(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestFilesystem_NewBackend_InvalidGlob(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.FilesystemBackendConfig{
		Name:      "fs",
		Type:      "filesystem",
		Root:      dir,
		Include:   []string{"[invalid"},
		URIScheme: "memory",
		Sandbox:   true,
	}
	_, err := NewFilesystemBackend(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid include pattern")
}

func TestFilesystem_Sandbox_SymlinkEscape(t *testing.T) {
	// Create a temp dir with a symlink pointing outside.
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	require.NoError(t, os.WriteFile(secret, []byte("top secret"), 0o600))

	link := filepath.Join(root, "escape.md")
	require.NoError(t, os.Symlink(secret, link))

	cfg := &config.FilesystemBackendConfig{
		Name:      "fs",
		Type:      "filesystem",
		Root:      root,
		Include:   []string{"**/*.md"},
		URIScheme: "memory",
		Sandbox:   true,
	}
	be, err := NewFilesystemBackend(cfg)
	require.NoError(t, err)

	_, err = be.Read(context.Background(), "memory://escape.md")
	require.Error(t, err, "symlink escape should be blocked")
	assert.True(t, errors.Is(err, model.ErrAccessViolation))
}

func TestSliceLines(t *testing.T) {
	const src = "one\ntwo\nthree\nfour\nfive"
	tests := []struct {
		name     string
		from, to int
		want     string
	}{
		{"whole", 0, 0, src},
		{"first two", 1, 2, "one\ntwo"},
		{"middle", 2, 4, "two\nthree\nfour"},
		{"clamp-high", 3, 999, "three\nfour\nfive"},
		{"out-of-range", 10, 20, ""},
		{"inverted", 4, 2, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SliceLines(src, tc.from, tc.to)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestContentTypeFor(t *testing.T) {
	cases := map[string]string{
		"x.md":       "text/markdown",
		"x.markdown": "text/markdown",
		"x.json":     "application/json",
		"x.yaml":     "application/yaml",
		"x.yml":      "application/yaml",
		"x.txt":      "text/plain",
		"x":          "text/plain",
	}
	for name, want := range cases {
		assert.Equal(t, want, contentTypeFor(name), "for %q", name)
	}
}
