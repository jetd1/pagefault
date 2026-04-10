package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Claude Code on MacBook": "claude-code-on-macbook",
		"iPhone 15":              "iphone-15",
		"   Leading Spaces":      "leading-spaces",
		"weird!!chars@@":         "weirdchars",
		"UPPER":                  "upper",
		"a--b":                   "a-b",
		"":                       "",
	}
	for in, want := range cases {
		assert.Equal(t, want, slugify(in), "slugify(%q)", in)
	}
}

func TestMaskToken(t *testing.T) {
	masked := maskToken("pf_thisisalongtoken_abcdefgh")
	assert.Contains(t, masked, "pf_thi")
	assert.Contains(t, masked, "efgh")
	assert.NotEqual(t, "pf_thisisalongtoken_abcdefgh", masked)

	short := maskToken("abc")
	assert.Equal(t, "***", short)
}

func TestTokenLifecycle_CreateListRevoke(t *testing.T) {
	dir := t.TempDir()
	tokensFile := filepath.Join(dir, "sub", "tokens.jsonl")

	// create
	err := runTokenCreate([]string{"--label", "Laptop", "--tokens-file", tokensFile})
	require.NoError(t, err)

	// file should exist and contain the record
	data, err := os.ReadFile(tokensFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Laptop")
	assert.Contains(t, string(data), "laptop")

	// create again with same label should fail (duplicate id)
	err = runTokenCreate([]string{"--label", "Laptop", "--tokens-file", tokensFile})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// create a second one with a different label
	err = runTokenCreate([]string{"--label", "Phone", "--tokens-file", tokensFile})
	require.NoError(t, err)

	// ls should show both
	records, err := readTokens(tokensFile)
	require.NoError(t, err)
	require.Len(t, records, 2)

	// revoke laptop
	err = runTokenRevoke([]string{"--tokens-file", tokensFile, "laptop"})
	require.NoError(t, err)

	records, err = readTokens(tokensFile)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "phone", records[0].ID)

	// revoke missing
	err = runTokenRevoke([]string{"--tokens-file", tokensFile, "nope"})
	require.Error(t, err)
}

func TestReadTokens_MissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	recs, err := readTokens(filepath.Join(dir, "does-not-exist.jsonl"))
	require.NoError(t, err)
	assert.Empty(t, recs)
}

func TestTokenCreate_RequiresLabel(t *testing.T) {
	dir := t.TempDir()
	err := runTokenCreate([]string{"--tokens-file", filepath.Join(dir, "t.jsonl")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--label")
}

func TestRunToken_UnknownSubcommand(t *testing.T) {
	err := runToken([]string{"frobnicate"})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "unknown"))
}

func TestRunToken_NoSubcommand(t *testing.T) {
	err := runToken(nil)
	require.Error(t, err)
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// whatever was written. It is used to inspect the output of commands that
// print directly (runTokenList, etc.) rather than returning a buffer.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		var buf [4096]byte
		var out strings.Builder
		for {
			n, rerr := r.Read(buf[:])
			if n > 0 {
				out.Write(buf[:n])
			}
			if rerr != nil {
				break
			}
		}
		done <- out.String()
	}()

	fn()
	_ = w.Close()
	return <-done
}

func TestRunTokenList_Empty(t *testing.T) {
	dir := t.TempDir()
	tokensFile := filepath.Join(dir, "tokens.jsonl")

	out := captureStdout(t, func() {
		require.NoError(t, runTokenList([]string{"--tokens-file", tokensFile}))
	})
	assert.Contains(t, out, "no tokens configured")
}

func TestRunTokenList_WithRecords(t *testing.T) {
	dir := t.TempDir()
	tokensFile := filepath.Join(dir, "tokens.jsonl")

	// Seed two tokens via the real create path so we exercise the full
	// write → read round-trip.
	require.NoError(t, runTokenCreate([]string{"--label", "Laptop", "--tokens-file", tokensFile}))
	require.NoError(t, runTokenCreate([]string{"--label", "Phone", "--tokens-file", tokensFile}))

	out := captureStdout(t, func() {
		require.NoError(t, runTokenList([]string{"--tokens-file", tokensFile}))
	})
	// The list should contain both IDs and the tabwriter header.
	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "LABEL")
	assert.Contains(t, out, "laptop")
	assert.Contains(t, out, "phone")
}

func TestResolveTokensFile_FromConfig(t *testing.T) {
	dir := t.TempDir()
	tokensFile := filepath.Join(dir, "tokens.jsonl")
	cfgPath := filepath.Join(dir, "pagefault.yaml")
	cfg := "" +
		"server:\n" +
		"  host: \"127.0.0.1\"\n" +
		"  port: 8444\n" +
		"auth:\n" +
		"  mode: \"bearer\"\n" +
		"  bearer:\n" +
		"    tokens_file: \"" + tokensFile + "\"\n" +
		"backends:\n" +
		"  - name: fs\n" +
		"    type: filesystem\n" +
		"    root: \"" + dir + "\"\n" +
		"    include: [\"**/*.md\"]\n" +
		"    uri_scheme: \"memory\"\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o600))

	got, err := resolveTokensFile(cfgPath, "")
	require.NoError(t, err)
	assert.Equal(t, tokensFile, got)

	// --tokens-file overrides --config.
	got2, err := resolveTokensFile(cfgPath, "/tmp/other.jsonl")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/other.jsonl", got2)

	// Both empty is an error.
	_, err = resolveTokensFile("", "")
	require.Error(t, err)
}
