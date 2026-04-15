package tool

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// TestBuildToolIcons_AllWireNames verifies every pf_* wire name has a
// matching icon built from the web/icons.svg sprite. A missing entry
// means symbolRe drifted from the sprite's naming convention — the
// fix is in web/icons.svg or the regex, not in this test.
func TestBuildToolIcons_AllWireNames(t *testing.T) {
	icons := buildToolIcons()
	require.NotNil(t, icons)
	for _, name := range []string{
		"pf_maps", "pf_load", "pf_scan", "pf_peek",
		"pf_fault", "pf_ps", "pf_poke",
	} {
		entries, ok := icons[name]
		require.True(t, ok, "no icon for %q", name)
		require.Len(t, entries, 1)
		assert.Equal(t, "image/svg+xml", entries[0].MIMEType)
		assert.Equal(t, []string{"any"}, entries[0].Sizes)
		require.True(t, strings.HasPrefix(entries[0].Src, "data:image/svg+xml;base64,"),
			"expected data URI, got %q", entries[0].Src)

		// Decode the payload and sanity-check it: explicit amber
		// stroke (no currentColor leak) and a dark tile. Also
		// confirms the glyph was extracted — each tool's svg
		// should contain at least one <path or <rect or <circle.
		b64 := strings.TrimPrefix(entries[0].Src, "data:image/svg+xml;base64,")
		raw, err := base64.StdEncoding.DecodeString(b64)
		require.NoError(t, err)
		s := string(raw)
		assert.NotContains(t, s, "currentColor",
			"currentColor leak in %q icon would break standalone rendering", name)
		assert.Contains(t, s, "#ff7e1f", "amber accent missing in %q icon", name)
		assert.Contains(t, s, "#121216", "dark tile missing in %q icon", name)
		assert.True(t,
			strings.Contains(s, "<path") || strings.Contains(s, "<rect") || strings.Contains(s, "<circle"),
			"no glyph primitives found in %q icon", name)
	}
}

// TestIconOptionFor_UnknownToolIsNoop verifies asking for an icon on an
// unregistered wire name yields a no-op option (the tool is simply
// registered without an icon) rather than panicking or returning nil
// that mcp-go would choke on.
func TestIconOptionFor_UnknownToolIsNoop(t *testing.T) {
	opt := iconOptionFor("pf_doesnotexist")
	require.NotNil(t, opt, "option must be non-nil even for unknown wire names")
	var tool mcppkg.Tool
	opt(&tool)
	assert.Empty(t, tool.Icons, "no-op option must not attach icons")
}
