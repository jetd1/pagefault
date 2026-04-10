package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

func TestSanitizeArgs_MasksSensitive(t *testing.T) {
	in := map[string]any{
		"query":         "pagefault",
		"token":         "pf_secret_value",
		"api_key":       "sk-xxx",
		"Authorization": "Bearer foo",
		"limit":         10,
	}
	out := SanitizeArgs(in)
	assert.Equal(t, "pagefault", out["query"])
	assert.Equal(t, "[REDACTED]", out["token"])
	assert.Equal(t, "[REDACTED]", out["api_key"])
	assert.Equal(t, "[REDACTED]", out["Authorization"])
	assert.Equal(t, 10, out["limit"])
}

func TestSanitizeArgs_EmptyReturnsEmpty(t *testing.T) {
	out := SanitizeArgs(nil)
	assert.Nil(t, out)
}

func TestSanitizeArgs_DoesNotMutate(t *testing.T) {
	in := map[string]any{"token": "xxx"}
	SanitizeArgs(in)
	assert.Equal(t, "xxx", in["token"], "input map must not be mutated")
}

func TestNewEntry_PopulatesFields(t *testing.T) {
	caller := model.Caller{ID: "laptop", Label: "Laptop"}
	start := time.Now().Add(-50 * time.Millisecond)
	e := NewEntry(caller, "search", map[string]any{"query": "x"}, start, 512, nil)

	assert.Equal(t, "laptop", e.CallerID)
	assert.Equal(t, "Laptop", e.CallerLabel)
	assert.Equal(t, "search", e.Tool)
	assert.Equal(t, "x", e.Args["query"])
	assert.Equal(t, 512, e.ResultSize)
	assert.GreaterOrEqual(t, e.DurationMS, int64(0))
	assert.Empty(t, e.Error)
}

func TestNewEntry_WithError(t *testing.T) {
	caller := model.Caller{ID: "x"}
	e := NewEntry(caller, "read", nil, time.Now(), 0, errors.New("boom"))
	assert.Equal(t, "boom", e.Error)
}

func TestJSONLLogger_WritesLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	lg, err := NewJSONLLogger(path)
	require.NoError(t, err)
	defer lg.Close()

	lg.Log(Entry{
		Timestamp: time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
		CallerID:  "laptop",
		Tool:      "search",
		Args:      map[string]any{"query": "x"},
	})
	require.NoError(t, lg.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 1)

	var parsed Entry
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &parsed))
	assert.Equal(t, "laptop", parsed.CallerID)
	assert.Equal(t, "search", parsed.Tool)
}

func TestJSONLLogger_Appends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	lg, err := NewJSONLLogger(path)
	require.NoError(t, err)
	lg.Log(Entry{Tool: "a"})
	require.NoError(t, lg.Close())

	lg2, err := NewJSONLLogger(path)
	require.NoError(t, err)
	lg2.Log(Entry{Tool: "b"})
	require.NoError(t, lg2.Close())

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
}

func TestStdoutLogger_WritesToWriter(t *testing.T) {
	var buf bytes.Buffer
	lg := NewWriterLogger(&buf)
	lg.Log(Entry{Tool: "read", CallerID: "x"})

	var parsed Entry
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &parsed))
	assert.Equal(t, "read", parsed.Tool)
	assert.Equal(t, "x", parsed.CallerID)
}

func TestNopLogger_Discards(t *testing.T) {
	lg := NopLogger{}
	lg.Log(Entry{Tool: "x"})
	require.NoError(t, lg.Close())
}

func TestNewFromConfig_Off(t *testing.T) {
	lg, err := NewFromConfig(config.AuditConfig{Mode: "off"})
	require.NoError(t, err)
	_, ok := lg.(NopLogger)
	assert.True(t, ok)
}

func TestNewFromConfig_Stdout(t *testing.T) {
	lg, err := NewFromConfig(config.AuditConfig{Mode: "stdout"})
	require.NoError(t, err)
	_, ok := lg.(*StdoutLogger)
	assert.True(t, ok)
}

func TestNewFromConfig_JSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	lg, err := NewFromConfig(config.AuditConfig{Mode: "jsonl", LogPath: path})
	require.NoError(t, err)
	_, ok := lg.(*JSONLLogger)
	assert.True(t, ok)
	require.NoError(t, lg.Close())
}

func TestNewFromConfig_InferDisabled(t *testing.T) {
	lg, err := NewFromConfig(config.AuditConfig{Enabled: false})
	require.NoError(t, err)
	_, ok := lg.(NopLogger)
	assert.True(t, ok)
}

func TestNewFromConfig_InferStdout(t *testing.T) {
	lg, err := NewFromConfig(config.AuditConfig{Enabled: true})
	require.NoError(t, err)
	_, ok := lg.(*StdoutLogger)
	assert.True(t, ok)
}

func TestNewFromConfig_InferJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "infer.jsonl")
	lg, err := NewFromConfig(config.AuditConfig{Enabled: true, LogPath: path})
	require.NoError(t, err)
	_, ok := lg.(*JSONLLogger)
	assert.True(t, ok)
	require.NoError(t, lg.Close())
}

func TestNewFromConfig_UnknownMode(t *testing.T) {
	_, err := NewFromConfig(config.AuditConfig{Mode: "wat"})
	require.Error(t, err)
}

func TestNewFromConfig_JSONLRequiresPath(t *testing.T) {
	_, err := NewFromConfig(config.AuditConfig{Mode: "jsonl"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "log_path")
}

func TestJSONLLogger_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	lg, err := NewJSONLLogger(path)
	require.NoError(t, err)
	defer lg.Close()

	const n = 100
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			lg.Log(Entry{Tool: "t", ResultSize: i})
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	require.NoError(t, lg.Close())

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Len(t, lines, n, "all entries should be written without interleaving")
}
