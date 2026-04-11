package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jet/pagefault/internal/auth"
	"github.com/jet/pagefault/internal/config"
)

// runOAuthClient dispatches `pagefault oauth-client <subcommand>`. The
// subcommand surface mirrors `pagefault token` deliberately so operators
// moving Claude Desktop over to OAuth2 only have to learn one mental
// model. See docs/config-doc.md → "auth.mode: oauth2" for the
// end-to-end wiring and the CHANGELOG 0.7.0 entry for the rationale.
func runOAuthClient(args []string) error {
	if len(args) == 0 {
		return errors.New("missing subcommand (create | ls | revoke)")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "create":
		return runOAuthClientCreate(rest)
	case "ls", "list":
		return runOAuthClientList(rest)
	case "revoke":
		return runOAuthClientRevoke(rest)
	default:
		return fmt.Errorf("unknown oauth-client subcommand: %s", sub)
	}
}

// oauthClientFlags adds the common --config / --clients-file flags. One
// of the two must be provided so we know where the JSONL file lives.
func oauthClientFlags(fs *flag.FlagSet) (*string, *string) {
	configPath := fs.String("config", "", "path to pagefault.yaml (reads auth.oauth2.clients_file from it)")
	clientsFile := fs.String("clients-file", "", "direct path to oauth-clients.jsonl")
	return configPath, clientsFile
}

// resolveClientsFile returns the effective clients file path, preferring
// --clients-file when set and otherwise reading auth.oauth2.clients_file
// from the config.
func resolveClientsFile(configPath, clientsFile string) (string, error) {
	if clientsFile != "" {
		return clientsFile, nil
	}
	if configPath == "" {
		return "", errors.New("either --config or --clients-file is required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	if cfg.Auth.OAuth2.ClientsFile == "" {
		return "", errors.New("config auth.oauth2.clients_file is empty; use --clients-file instead")
	}
	return cfg.Auth.OAuth2.ClientsFile, nil
}

// ─────────────────── create ───────────────────

func runOAuthClientCreate(args []string) error {
	fs := flag.NewFlagSet("oauth-client create", flag.ContinueOnError)
	label := fs.String("label", "", "human-readable label for the client (required)")
	id := fs.String("id", "", "custom client id (default: derived from label)")
	scopes := fs.String("scopes", "", "space-separated allowed scopes (default: mcp)")
	configPath, clientsFile := oauthClientFlags(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *label == "" {
		return errors.New("--label is required")
	}

	path, err := resolveClientsFile(*configPath, *clientsFile)
	if err != nil {
		return err
	}

	records, err := readOAuthClients(path)
	if err != nil {
		return err
	}

	idVal := *id
	if idVal == "" {
		idVal = slugify(*label)
	}
	if idVal == "" {
		return errors.New("could not derive id from label; use --id to override")
	}
	for _, r := range records {
		if r.ID == idVal {
			return fmt.Errorf("id %q already exists; choose another --id", idVal)
		}
	}

	secret, err := auth.GenerateClientSecret()
	if err != nil {
		return err
	}
	hash, err := auth.HashClientSecret(secret)
	if err != nil {
		return err
	}

	var scopeList []string
	if *scopes != "" {
		scopeList = strings.Fields(*scopes)
	} else {
		scopeList = []string{"mcp"}
	}

	rec := auth.ClientRecord{
		ID:         idVal,
		Label:      *label,
		SecretHash: hash,
		Scopes:     scopeList,
		Metadata: map[string]any{
			"created_at": time.Now().UTC().Format(time.RFC3339),
		},
	}
	records = append(records, rec)

	if err := writeOAuthClients(path, records); err != nil {
		return err
	}

	fmt.Printf("created oauth2 client\n")
	fmt.Printf("  id:     %s\n", idVal)
	fmt.Printf("  label:  %s\n", *label)
	fmt.Printf("  scopes: %s\n", strings.Join(scopeList, " "))
	fmt.Printf("  secret: %s\n", secret)
	fmt.Printf("\nRecord this secret now — it will not be shown again.\n")
	fmt.Printf("Use the id as the OAuth2 Client ID and the secret as the OAuth2 Client Secret.\n")
	return nil
}

// ─────────────────── list ───────────────────

func runOAuthClientList(args []string) error {
	fs := flag.NewFlagSet("oauth-client ls", flag.ContinueOnError)
	configPath, clientsFile := oauthClientFlags(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := resolveClientsFile(*configPath, *clientsFile)
	if err != nil {
		return err
	}
	records, err := readOAuthClients(path)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		fmt.Println("no oauth2 clients configured")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLABEL\tSCOPES\tCREATED")
	for _, r := range records {
		created := ""
		if r.Metadata != nil {
			if c, ok := r.Metadata["created_at"].(string); ok {
				created = c
			}
		}
		scopes := strings.Join(r.Scopes, " ")
		if scopes == "" {
			scopes = "(default)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, r.Label, scopes, created)
	}
	return tw.Flush()
}

// ─────────────────── revoke ───────────────────

func runOAuthClientRevoke(args []string) error {
	fs := flag.NewFlagSet("oauth-client revoke", flag.ContinueOnError)
	configPath, clientsFile := oauthClientFlags(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: pagefault oauth-client revoke <id>")
	}
	target := fs.Arg(0)

	path, err := resolveClientsFile(*configPath, *clientsFile)
	if err != nil {
		return err
	}
	records, err := readOAuthClients(path)
	if err != nil {
		return err
	}

	out := make([]auth.ClientRecord, 0, len(records))
	found := false
	for _, r := range records {
		if r.ID == target {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return fmt.Errorf("client %q not found", target)
	}
	if err := writeOAuthClients(path, out); err != nil {
		return err
	}
	fmt.Printf("revoked oauth2 client %q\n", target)
	fmt.Printf("Note: any access tokens already issued to this client remain valid until the server restarts\n")
	fmt.Printf("or they expire (default 1 hour). Restart pagefault to invalidate them immediately.\n")
	return nil
}

// ─────────────────── helpers ───────────────────

// readOAuthClients reads the JSONL clients file. A missing file is
// treated as empty; any other error is returned.
func readOAuthClients(path string) ([]auth.ClientRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return auth.ParseClientsJSONL(data)
}

// writeOAuthClients writes records atomically via a temp file + rename.
// Creates any missing parent directories.
func writeOAuthClients(path string, records []auth.ClientRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, r := range records {
		if err := enc.Encode(&r); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
