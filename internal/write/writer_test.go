package write

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilesystemWriter_AppendCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")

	w := NewFilesystemWriter(LockNone)
	n, err := w.Append(context.Background(), path, "hello\n")
	require.NoError(t, err)
	assert.Equal(t, 6, n)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(got))
}

func TestFilesystemWriter_AppendStacks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.md")

	w := NewFilesystemWriter(LockFlock)
	_, err := w.Append(context.Background(), path, "one\n")
	require.NoError(t, err)
	_, err = w.Append(context.Background(), path, "two\n")
	require.NoError(t, err)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "one\ntwo\n", string(got))
}

func TestFilesystemWriter_AppendCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "nested", "note.md")

	w := NewFilesystemWriter(LockFlock)
	n, err := w.Append(context.Background(), path, "content")
	require.NoError(t, err)
	assert.Equal(t, 7, n)

	// Parent dir should exist.
	info, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestFilesystemWriter_EmptyPath(t *testing.T) {
	w := NewFilesystemWriter(LockNone)
	_, err := w.Append(context.Background(), "", "x")
	require.Error(t, err)
}

func TestFilesystemWriter_CancelledContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := NewFilesystemWriter(LockNone)
	_, err := w.Append(ctx, path, "x")
	require.Error(t, err)
}

func TestFilesystemWriter_ConcurrentAppend(t *testing.T) {
	// Fire N goroutines appending to the same file and verify total
	// bytes and line count. The per-writer mutex + flock cooperate to
	// serialise writes so no line is split.
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.md")

	w := NewFilesystemWriter(LockFlock)
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := w.Append(context.Background(), path, "line\n")
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, n*len("line\n"), len(data))
}

func TestFilesystemWriter_DefaultLockMode(t *testing.T) {
	w := NewFilesystemWriter("")
	assert.Equal(t, LockFlock, w.lockMode)
}
