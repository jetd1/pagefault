package backend

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file holds the small helpers shared by the HTTP-shaped backends:
// `http.go` (generic search) and `subagent_http.go` (pf_fault subagent).
// Keeping them here rather than in one backend's file makes the "who
// owns this helper" question trivial and prevents cross-file imports
// within the same package.

// renderTemplate performs simple `{key}` → value substitution. Missing
// keys are left untouched. We use this instead of text/template to keep
// the templating contract small and obvious — every call site in the
// backend package only needs literal placeholder replacement, and
// text/template's behaviour around missing keys / type coercion is
// more surface than we need.
func renderTemplate(tmpl string, vars map[string]string) string {
	if tmpl == "" {
		return ""
	}
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

// jsonEscape escapes a string for safe embedding inside a JSON string
// literal (no enclosing quotes). Uses encoding/json for correctness —
// important when caller-supplied task/query strings contain quotes,
// newlines, or non-ASCII characters.
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	// Strip the surrounding double quotes that json.Marshal adds.
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		return string(b[1 : len(b)-1])
	}
	return string(b)
}

// walkPath walks a nested map[string]any along a dotted path. It also
// accepts a leading "$." for JSONPath-like ergonomics. Returns the
// value and true on success, zero-value and false on any miss.
// An empty path returns the root unchanged.
func walkPath(root any, path string) (any, bool) {
	path = strings.TrimPrefix(path, "$.")
	if path == "" {
		return root, true
	}
	cur := root
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// extractResponse parses raw JSON and returns the value at the given
// dotted path as a string. If path is empty, the raw body (trimmed) is
// returned as-is without parsing. Non-string leaf values are
// JSON-re-encoded.
func extractResponse(raw []byte, path string) (string, error) {
	if path == "" {
		return strings.TrimSpace(string(raw)), nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	val, ok := walkPath(decoded, path)
	if !ok {
		return "", fmt.Errorf("response path %q not found", path)
	}
	if s, ok := val.(string); ok {
		return s, nil
	}
	out, err := json.Marshal(val)
	if err != nil {
		return "", fmt.Errorf("re-encode response value: %w", err)
	}
	return string(out), nil
}
