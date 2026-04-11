package backend

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"jetd.one/pagefault/internal/config"
	"jetd.one/pagefault/internal/model"
)

func TestNewSubprocessBackend_Validation(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		_, err := NewSubprocessBackend(nil)
		require.Error(t, err)
	})
	t.Run("empty-command", func(t *testing.T) {
		_, err := NewSubprocessBackend(&config.SubprocessBackendConfig{Name: "x", Type: "subprocess"})
		require.Error(t, err)
	})
	t.Run("unknown-parse", func(t *testing.T) {
		_, err := NewSubprocessBackend(&config.SubprocessBackendConfig{
			Name: "x", Type: "subprocess", Command: "echo x", Parse: "wat",
		})
		require.Error(t, err)
	})
	t.Run("ok", func(t *testing.T) {
		b, err := NewSubprocessBackend(&config.SubprocessBackendConfig{
			Name: "rg", Type: "subprocess", Command: "echo x", Parse: "plain",
		})
		require.NoError(t, err)
		assert.Equal(t, "rg", b.Name())
	})
}

func TestSubprocessBackend_Read_Unsupported(t *testing.T) {
	b, err := NewSubprocessBackend(&config.SubprocessBackendConfig{
		Name: "x", Type: "subprocess", Command: "echo y",
	})
	require.NoError(t, err)
	_, err = b.Read(context.Background(), "x://y")
	assert.True(t, errors.Is(err, model.ErrResourceNotFound))

	list, err := b.ListResources(context.Background())
	require.NoError(t, err)
	assert.Nil(t, list)
}

func TestSubprocessBackend_Search_EmptyQuery(t *testing.T) {
	b, err := NewSubprocessBackend(&config.SubprocessBackendConfig{
		Name: "x", Type: "subprocess", Command: "echo x",
	})
	require.NoError(t, err)

	res, err := b.Search(context.Background(), "", 10)
	require.NoError(t, err)
	assert.Nil(t, res)
}

func TestSubprocessBackend_Search_Plain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("echo semantics differ on Windows")
	}
	b, err := NewSubprocessBackend(&config.SubprocessBackendConfig{
		Name: "x", Type: "subprocess",
		Command: "echo {query}",
		Parse:   "plain",
	})
	require.NoError(t, err)

	res, err := b.Search(context.Background(), "hello", 10)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, "hello", res[0].Snippet)
	assert.Equal(t, "x", res[0].Metadata["backend"])
}

func TestSubprocessBackend_Search_GrepParse(t *testing.T) {
	input := []byte("a.md:12:first match\nb.md:34:second\nbad-line\nc.md:5:third\n")
	out := parseGrep(input, 10, "rg")
	require.Len(t, out, 3)
	assert.Equal(t, "a.md", out[0].URI)
	assert.Equal(t, "first match", out[0].Snippet)
	assert.Equal(t, 12, out[0].Metadata["line"])
	assert.Equal(t, "b.md", out[1].URI)
	assert.Equal(t, "c.md", out[2].URI)
}

func TestSubprocessBackend_Search_GrepParse_Limit(t *testing.T) {
	input := []byte("a:1:x\nb:2:y\nc:3:z\n")
	out := parseGrep(input, 2, "x")
	assert.Len(t, out, 2)
}

func TestSubprocessBackend_Search_RipgrepJSONParse(t *testing.T) {
	// Realistic ripgrep --json output: begin/match/end per file.
	jsonl := []byte(`{"type":"begin","data":{"path":{"text":"notes.md"}}}
{"type":"match","data":{"path":{"text":"notes.md"},"lines":{"text":"hello world\n"},"line_number":3}}
{"type":"match","data":{"path":{"text":"notes.md"},"lines":{"text":"hello again\n"},"line_number":7}}
{"type":"end","data":{"path":{"text":"notes.md"}}}
`)
	out := parseRipgrepJSON(jsonl, 10, "rg")
	require.Len(t, out, 2)
	assert.Equal(t, "notes.md", out[0].URI)
	assert.Equal(t, "hello world", out[0].Snippet)
	assert.Equal(t, 3, out[0].Metadata["line"])
	assert.Equal(t, "hello again", out[1].Snippet)
	assert.Equal(t, 7, out[1].Metadata["line"])
}

func TestSubprocessBackend_Search_RipgrepJSONParse_Limit(t *testing.T) {
	jsonl := []byte(`{"type":"match","data":{"path":{"text":"a"},"lines":{"text":"x"},"line_number":1}}
{"type":"match","data":{"path":{"text":"b"},"lines":{"text":"y"},"line_number":2}}
{"type":"match","data":{"path":{"text":"c"},"lines":{"text":"z"},"line_number":3}}
`)
	out := parseRipgrepJSON(jsonl, 2, "rg")
	assert.Len(t, out, 2)
}

func TestSubprocessBackend_Search_RipgrepJSONParse_SkipsBadLines(t *testing.T) {
	jsonl := []byte("not-json\n{\"type\":\"match\",\"data\":{\"path\":{\"text\":\"a\"},\"lines\":{\"text\":\"x\"},\"line_number\":1}}\n")
	out := parseRipgrepJSON(jsonl, 10, "rg")
	require.Len(t, out, 1)
	assert.Equal(t, "a", out[0].URI)
}

func TestSubprocessBackend_Search_NoMatchExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("grep not portable to Windows")
	}
	// grep exits 1 on no match; the backend should treat that as an
	// empty result set, not an error.
	b, err := NewSubprocessBackend(&config.SubprocessBackendConfig{
		Name: "g", Type: "subprocess",
		Command: "grep --no-messages nonsense-xyzzy-abc123 /dev/null",
		Parse:   "grep",
	})
	require.NoError(t, err)

	out, err := b.Search(context.Background(), "x", 10)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestSubprocessBackend_Search_CommandNotFound(t *testing.T) {
	b, err := NewSubprocessBackend(&config.SubprocessBackendConfig{
		Name: "nope", Type: "subprocess",
		Command: "this-command-definitely-does-not-exist-xyz123 {query}",
	})
	require.NoError(t, err)

	_, err = b.Search(context.Background(), "q", 10)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrBackendUnavailable))
}
