package tool

import (
	"encoding/base64"
	"regexp"
	"strings"

	mcppkg "github.com/mark3labs/mcp-go/mcp"

	"jetd.one/pagefault/web"
)

// toolIcons maps pf_* wire names to a standalone, self-contained SVG
// data URI suitable for the MCP `Tool.Icons` field advertised in the
// tools/list response. Each entry wraps the matching 24×24 glyph from
// web/icons.svg (the canonical landing-site tool sprite) in a rounded
// dark tile with the brand amber (#ff7e1f) replacing the sprite's
// currentColor, so the icon renders correctly regardless of the MCP
// client's CSS context.
//
// Keyed by wire name (pf_maps, pf_load, …) so registerX in mcp.go
// can look up its glyph with a single map access. Nil map entry
// means "no icon" — registerX degrades to an icon-less tool rather
// than refusing to register.
var toolIcons = buildToolIcons()

// symbolRe matches one <symbol id="NAME" viewBox="0 0 24 24">…</symbol>
// block in web/icons.svg. The (?s) flag makes `.` cross newlines so
// the inner content survives the multi-line glyph bodies. Captures
// the unprefixed glyph name (maps/load/scan/…) and the inner markup.
var symbolRe = regexp.MustCompile(`(?s)<symbol id="([a-z]+)" viewBox="0 0 24 24">\s*(.+?)\s*</symbol>`)

// buildToolIcons parses web/icons.svg once at package init time and
// returns a map of pf_* wire names to a single-entry []mcp.Icon
// slice. The embed is guaranteed populated by //go:embed in
// web/embed.go, so an empty map here means the sprite format drifted
// from symbolRe — callers treat that as "no icon" rather than crash.
func buildToolIcons() map[string][]mcppkg.Icon {
	data, err := web.Files.ReadFile("icons.svg")
	if err != nil {
		return nil
	}
	out := make(map[string][]mcppkg.Icon)
	for _, m := range symbolRe.FindAllSubmatch(data, -1) {
		glyph := strings.ReplaceAll(string(m[2]), "currentColor", "#ff7e1f")
		doc := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">` +
			`<rect width="24" height="24" rx="4" fill="#121216"/>` +
			glyph +
			`</svg>`
		uri := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(doc))
		wireName := "pf_" + string(m[1])
		out[wireName] = []mcppkg.Icon{{
			Src:      uri,
			MIMEType: "image/svg+xml",
			Sizes:    []string{"any"},
		}}
	}
	return out
}

// iconOptionFor returns the mcp-go ToolOption that attaches the
// per-tool icon to a tool at registration time. Tools without a
// registered glyph (unknown wire names, or a parse failure in
// buildToolIcons) get no icon — the option is a no-op in that case.
func iconOptionFor(wireName string) mcppkg.ToolOption {
	icons, ok := toolIcons[wireName]
	if !ok || len(icons) == 0 {
		return func(*mcppkg.Tool) {}
	}
	return mcppkg.WithToolIcons(icons...)
}
