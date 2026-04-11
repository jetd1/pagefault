package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/auth"
	"github.com/jet/pagefault/internal/config"
)

func TestOAuthClientLifecycle_CreateListRevoke(t *testing.T) {
	dir := t.TempDir()
	clientsFile := filepath.Join(dir, "sub", "oauth-clients.jsonl")

	// create
	err := runOAuthClientCreate([]string{"--label", "Claude Desktop", "--clients-file", clientsFile})
	require.NoError(t, err)

	// file should exist and contain the record with the slugified id
	data, err := os.ReadFile(clientsFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "claude-desktop")
	assert.Contains(t, string(data), "Claude Desktop")
	// The secret itself should NOT appear in the file — only the bcrypt hash.
	assert.NotContains(t, string(data), "pf_cs_", "raw secret should not be stored")
	assert.Contains(t, string(data), "$2", "bcrypt hash prefix should be present")

	// create again with same label → duplicate id
	err = runOAuthClientCreate([]string{"--label", "Claude Desktop", "--clients-file", clientsFile})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// create a second client
	err = runOAuthClientCreate([]string{"--label", "Phone", "--clients-file", clientsFile})
	require.NoError(t, err)

	// ls should show both
	records, err := readOAuthClients(clientsFile)
	require.NoError(t, err)
	require.Len(t, records, 2)

	// revoke claude-desktop
	err = runOAuthClientRevoke([]string{"--clients-file", clientsFile, "claude-desktop"})
	require.NoError(t, err)

	records, err = readOAuthClients(clientsFile)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "phone", records[0].ID)

	// revoke missing
	err = runOAuthClientRevoke([]string{"--clients-file", clientsFile, "nope"})
	require.Error(t, err)
}

func TestOAuthClientCreate_StoredHashVerifies(t *testing.T) {
	dir := t.TempDir()
	clientsFile := filepath.Join(dir, "oauth-clients.jsonl")

	out := captureStdout(t, func() {
		require.NoError(t, runOAuthClientCreate([]string{
			"--label", "Test", "--clients-file", clientsFile,
		}))
	})

	// The printed secret is on a "secret: <value>" line; extract it
	// so we can verify the stored hash accepts it.
	var secret string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "secret:") {
			secret = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "secret:"))
			break
		}
	}
	require.NotEmpty(t, secret, "expected a secret: line in CLI output")

	records, err := readOAuthClients(clientsFile)
	require.NoError(t, err)
	require.Len(t, records, 1)

	// Parse the JSONL hash and verify it accepts the printed secret.
	hash := records[0].SecretHash
	require.NotEmpty(t, hash)
	// Wrong secret rejected.
	assert.Error(t, bcryptCompare(t, hash, "totally-wrong"))
	// Right secret accepted.
	assert.NoError(t, bcryptCompare(t, hash, secret))
}

// bcryptCompare is a test-only shim that round-trips a candidate
// secret through the OAuth2 provider's IssueToken path. A nil return
// means the stored hash accepted the candidate.
func bcryptCompare(t *testing.T, hash, candidate string) error {
	t.Helper()
	dir := t.TempDir()
	clientsFile := filepath.Join(dir, "clients.jsonl")
	require.NoError(t, os.WriteFile(clientsFile, []byte(
		`{"id":"probe","label":"Probe","secret_hash":"`+hash+`","scopes":["mcp"]}`+"\n"), 0o600))
	cfg := config.AuthConfig{
		Mode: "oauth2",
		OAuth2: config.OAuth2Config{
			ClientsFile:           clientsFile,
			AccessTokenTTLSeconds: 3600,
		},
	}
	p, err := auth.NewOAuth2Provider(cfg)
	require.NoError(t, err)
	_, err = p.IssueToken(context.Background(), "probe", candidate, nil)
	return err
}

func TestResolveClientsFile_FromConfig(t *testing.T) {
	dir := t.TempDir()
	clientsFile := filepath.Join(dir, "oauth-clients.jsonl")
	cfgPath := filepath.Join(dir, "pagefault.yaml")
	cfg := "" +
		"server:\n" +
		"  host: \"127.0.0.1\"\n" +
		"  port: 8444\n" +
		"auth:\n" +
		"  mode: \"oauth2\"\n" +
		"  oauth2:\n" +
		"    clients_file: \"" + clientsFile + "\"\n" +
		"backends:\n" +
		"  - name: fs\n" +
		"    type: filesystem\n" +
		"    root: \"" + dir + "\"\n" +
		"    include: [\"**/*.md\"]\n" +
		"    uri_scheme: \"memory\"\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o600))

	got, err := resolveClientsFile(cfgPath, "")
	require.NoError(t, err)
	assert.Equal(t, clientsFile, got)

	// --clients-file overrides --config.
	got2, err := resolveClientsFile(cfgPath, "/tmp/other.jsonl")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/other.jsonl", got2)

	// Both empty is an error.
	_, err = resolveClientsFile("", "")
	require.Error(t, err)
}

func TestOAuthClientCreate_RequiresLabel(t *testing.T) {
	dir := t.TempDir()
	err := runOAuthClientCreate([]string{"--clients-file", filepath.Join(dir, "c.jsonl")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--label")
}

func TestRunOAuthClient_UnknownSubcommand(t *testing.T) {
	err := runOAuthClient([]string{"frobnicate"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown")
}

func TestRunOAuthClient_NoSubcommand(t *testing.T) {
	err := runOAuthClient(nil)
	require.Error(t, err)
}

func TestRunOAuthClientList_Empty(t *testing.T) {
	dir := t.TempDir()
	clientsFile := filepath.Join(dir, "oauth-clients.jsonl")

	out := captureStdout(t, func() {
		require.NoError(t, runOAuthClientList([]string{"--clients-file", clientsFile}))
	})
	assert.Contains(t, out, "no oauth2 clients configured")
}

func TestRunOAuthClientList_WithRecords(t *testing.T) {
	dir := t.TempDir()
	clientsFile := filepath.Join(dir, "oauth-clients.jsonl")

	require.NoError(t, runOAuthClientCreate([]string{"--label", "Desktop", "--clients-file", clientsFile}))
	require.NoError(t, runOAuthClientCreate([]string{"--label", "Phone", "--scopes", "mcp mcp.read", "--clients-file", clientsFile}))

	out := captureStdout(t, func() {
		require.NoError(t, runOAuthClientList([]string{"--clients-file", clientsFile}))
	})
	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "LABEL")
	assert.Contains(t, out, "desktop")
	assert.Contains(t, out, "phone")
	assert.Contains(t, out, "mcp.read")
}
