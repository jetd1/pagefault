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
