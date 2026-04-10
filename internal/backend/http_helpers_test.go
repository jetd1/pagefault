package backend

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderTemplate(t *testing.T) {
	out := renderTemplate(`{"task":"{task}","id":"{agent_id}"}`, map[string]string{
		"task":     "hello",
		"agent_id": "alpha",
	})
	assert.Equal(t, `{"task":"hello","id":"alpha"}`, out)

	// Empty template stays empty.
	assert.Equal(t, "", renderTemplate("", nil))

	// Missing keys stay as-is — caller's problem, not ours.
	assert.Equal(t, "{unset}", renderTemplate("{unset}", nil))

	// Multiple occurrences of the same key are all replaced.
	assert.Equal(t, "a-a-a", renderTemplate("{x}-{x}-{x}", map[string]string{"x": "a"}))
}

func TestJsonEscape(t *testing.T) {
	assert.Equal(t, `hello \"world\"`, jsonEscape(`hello "world"`))
	assert.Equal(t, `line1\nline2`, jsonEscape("line1\nline2"))
	assert.Equal(t, `tab\there`, jsonEscape("tab\there"))
	assert.Equal(t, "plain", jsonEscape("plain"))
	// Unicode survives untouched (json.Marshal keeps it as runes).
	assert.Equal(t, "你好", jsonEscape("你好"))
	// Empty string is the empty escaped form.
	assert.Equal(t, "", jsonEscape(""))
}

func TestWalkPath(t *testing.T) {
	obj := map[string]any{
		"a": map[string]any{
			"b":    map[string]any{"c": "leaf"},
			"list": []any{1, 2, 3},
		},
		"top": "value",
	}

	// Multi-level dotted path.
	v, ok := walkPath(obj, "a.b.c")
	require.True(t, ok)
	assert.Equal(t, "leaf", v)

	// `$.` prefix (JSONPath ergonomics) tolerated.
	v, ok = walkPath(obj, "$.a.b.c")
	require.True(t, ok)
	assert.Equal(t, "leaf", v)

	// Top-level single-segment path.
	v, ok = walkPath(obj, "top")
	require.True(t, ok)
	assert.Equal(t, "value", v)

	// Empty path returns the root unchanged.
	v, ok = walkPath(obj, "")
	require.True(t, ok)
	assert.Equal(t, obj, v)

	// Empty path after stripping `$.` also returns root.
	v, ok = walkPath(obj, "$.")
	require.True(t, ok)
	assert.Equal(t, obj, v)

	// Missing leaf.
	_, ok = walkPath(obj, "a.missing")
	assert.False(t, ok)

	// Descending into a non-object fails cleanly.
	_, ok = walkPath(obj, "a.list.0")
	assert.False(t, ok)

	// Non-map root.
	_, ok = walkPath("string-root", "a")
	assert.False(t, ok)
}

func TestExtractResponse_EmptyPathReturnsRaw(t *testing.T) {
	out, err := extractResponse([]byte("  just text\n"), "")
	require.NoError(t, err)
	assert.Equal(t, "just text", out, "whitespace should be trimmed")
}

func TestExtractResponse_StringLeaf(t *testing.T) {
	body := []byte(`{"result": {"answer": "42"}}`)
	out, err := extractResponse(body, "result.answer")
	require.NoError(t, err)
	assert.Equal(t, "42", out)
}

func TestExtractResponse_NumericLeafIsJSONEncoded(t *testing.T) {
	// Non-string leaves get JSON-re-encoded.
	body := []byte(`{"score": 0.97}`)
	out, err := extractResponse(body, "score")
	require.NoError(t, err)
	assert.Equal(t, "0.97", out)
}

func TestExtractResponse_ObjectLeafIsJSONEncoded(t *testing.T) {
	body := []byte(`{"data": {"k": "v"}}`)
	out, err := extractResponse(body, "data")
	require.NoError(t, err)
	assert.JSONEq(t, `{"k":"v"}`, out)
}

func TestExtractResponse_PathMissing(t *testing.T) {
	body := []byte(`{"other": "field"}`)
	_, err := extractResponse(body, "result.answer")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExtractResponse_BadJSON(t *testing.T) {
	_, err := extractResponse([]byte("not-json-at-all"), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode response")
}

func TestExtractResponse_DollarPrefix(t *testing.T) {
	body := []byte(`{"result": "ok"}`)
	out, err := extractResponse(body, "$.result")
	require.NoError(t, err)
	assert.Equal(t, "ok", out)
}
