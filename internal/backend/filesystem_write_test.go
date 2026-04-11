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

// newTestWritableBackend returns a filesystem backend rooted in a
// tempdir with write mode enabled and a single allowed pattern.
func newTestWritableBackend(t *testing.T, opts ...func(*config.FilesystemBackendConfig)) *FilesystemBackend {
	t.Helper()
	root := t.TempDir()
	cfg := &config.FilesystemBackendConfig{
		Name:         "fs",
		Type:         "filesystem",
		Root:         root,
		Include:      []string{"**/*.md"},
		URIScheme:    "memory",
		Sandbox:      true,
		Writable:     true,
		WritePaths:   []string{"memory://notes/*.md"},
		WriteMode:    "append",
		MaxEntrySize: 200,
		FileLocking:  "flock",
	}
	for _, opt := range opts {
		opt(cfg)
	}
	be, err := NewFilesystemBackend(cfg)
	require.NoError(t, err)
	return be
}

func TestFilesystem_Write_HappyPath(t *testing.T) {
	be := newTestWritableBackend(t)
	ctx := context.Background()

	n, err := be.Write(ctx, "memory://notes/today.md", "hello\n")
	require.NoError(t, err)
	assert.Equal(t, 6, n)

	// The file was created and contains what we wrote.
	got, err := os.ReadFile(filepath.Join(be.Root(), "notes/today.md"))
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(got))

	// Second write appends.
	_, err = be.Write(ctx, "memory://notes/today.md", "more\n")
	require.NoError(t, err)
	got, err = os.ReadFile(filepath.Join(be.Root(), "notes/today.md"))
	require.NoError(t, err)
	assert.Equal(t, "hello\nmore\n", string(got))
}

func TestFilesystem_Write_ReadOnlyBackendRejects(t *testing.T) {
	// Default backend from filesystem_test.go is read-only (no
	// writable flag). newTestBackend covers that path; use it here.
	be := newTestBackend(t)
	_, err := be.Write(context.Background(), "memory://README.md", "new content")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAccessViolation))
}

func TestFilesystem_Write_NotInIncludeRejects(t *testing.T) {
	be := newTestWritableBackend(t)
	_, err := be.Write(context.Background(), "memory://outside.txt", "content")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAccessViolation))
}

func TestFilesystem_Write_NotInWritePathsRejects(t *testing.T) {
	be := newTestWritableBackend(t)
	// Matches `**/*.md` include but not `notes/*.md` write_paths.
	_, err := be.Write(context.Background(), "memory://other.md", "content")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAccessViolation))
}

func TestFilesystem_Write_EmptyWritePathsIsAllow(t *testing.T) {
	// Explicit empty write_paths defaults to "allow every includable URI".
	be := newTestWritableBackend(t, func(c *config.FilesystemBackendConfig) {
		c.WritePaths = nil
	})
	_, err := be.Write(context.Background(), "memory://any.md", "content")
	require.NoError(t, err)
}

func TestFilesystem_Write_MaxEntrySizeNotEnforcedAtBackend(t *testing.T) {
	// As of 0.5.1, max_entry_size is enforced by the pf_poke tool
	// layer against the *raw* caller content, not by the backend
	// against the (possibly entry-template-wrapped) bytes. The
	// backend exposes the limit via MaxEntrySize() for the tool
	// layer to consult but does not itself reject oversize writes —
	// otherwise format:"entry" would silently eat 40–60 bytes of
	// the caller's budget. The end-to-end cap is covered by
	// TestHandleWrite_DirectContentTooLarge in the tool package.
	be := newTestWritableBackend(t, func(c *config.FilesystemBackendConfig) {
		c.MaxEntrySize = 8
	})
	// 9 bytes > cap — backend accepts it because cap enforcement
	// moved to the tool layer.
	_, err := be.Write(context.Background(), "memory://notes/a.md", "123456789")
	require.NoError(t, err)
	// Accessor still reports the configured cap so the tool layer
	// can consult it.
	assert.Equal(t, 8, be.MaxEntrySize())
}

func TestFilesystem_Write_PathTraversalRejected(t *testing.T) {
	be := newTestWritableBackend(t)
	_, err := be.Write(context.Background(), "memory://../../etc/passwd", "oops")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAccessViolation))
}

func TestFilesystem_Write_SymlinkedParentEscapesRootRejected(t *testing.T) {
	be := newTestWritableBackend(t)
	outside := t.TempDir()
	// Create a symlinked parent `notes` → outside so writing into it
	// would escape root on MkdirAll. resolveWritePath should catch
	// this at the first-existing-parent step.
	_ = os.Remove(filepath.Join(be.Root(), "notes"))
	err := os.Symlink(outside, filepath.Join(be.Root(), "notes"))
	require.NoError(t, err)

	_, err = be.Write(context.Background(), "memory://notes/leak.md", "oops")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrAccessViolation))

	// Verify nothing was written outside root.
	_, statErr := os.Stat(filepath.Join(outside, "leak.md"))
	assert.True(t, os.IsNotExist(statErr), "write should not have escaped root")
}

func TestFilesystem_Write_InvalidURIScheme(t *testing.T) {
	be := newTestWritableBackend(t)
	_, err := be.Write(context.Background(), "other://notes/a.md", "x")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidRequest))
}

func TestFilesystem_WritableAccessors(t *testing.T) {
	be := newTestWritableBackend(t, func(c *config.FilesystemBackendConfig) {
		c.WriteMode = "any"
		c.MaxEntrySize = 500
	})
	assert.True(t, be.Writable())
	assert.Equal(t, "any", be.WriteMode())
	assert.Equal(t, 500, be.MaxEntrySize())
	assert.Equal(t, []string{"memory://notes/*.md"}, be.WritePaths())
}

func TestFilesystem_ReadOnlyAccessors(t *testing.T) {
	be := newTestBackend(t)
	assert.False(t, be.Writable())
	assert.Empty(t, be.WritePaths())
	assert.Equal(t, "", be.WriteMode())
	assert.Equal(t, 0, be.MaxEntrySize())
}

func TestFilesystem_Write_InvalidWritePathPattern(t *testing.T) {
	root := t.TempDir()
	cfg := &config.FilesystemBackendConfig{
		Name:       "fs",
		Type:       "filesystem",
		Root:       root,
		URIScheme:  "memory",
		Sandbox:    true,
		Writable:   true,
		WritePaths: []string{"[bad-pattern"},
	}
	_, err := NewFilesystemBackend(cfg)
	require.Error(t, err)
}
