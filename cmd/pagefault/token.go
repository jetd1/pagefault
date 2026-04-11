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

	"jetd.one/pagefault/internal/auth"
	"jetd.one/pagefault/internal/config"
)

// runToken dispatches `pagefault token <subcommand>`.
func runToken(args []string) error {
	if len(args) == 0 {
		return errors.New("missing subcommand (create | ls | revoke)")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "create":
		return runTokenCreate(rest)
	case "ls", "list":
		return runTokenList(rest)
	case "revoke":
		return runTokenRevoke(rest)
	default:
		return fmt.Errorf("unknown token subcommand: %s", sub)
	}
}

// tokenFlags adds the common --config / --tokens-file flags to a FlagSet.
// Exactly one of the two must be provided.
func tokenFlags(fs *flag.FlagSet) (*string, *string) {
	configPath := fs.String("config", "", "path to pagefault.yaml (reads tokens_file from it)")
	tokensFile := fs.String("tokens-file", "", "direct path to tokens.jsonl")
	return configPath, tokensFile
}

// resolveTokensFile returns the effective tokens file path, preferring
// --tokens-file when set and otherwise reading auth.bearer.tokens_file from
// the config.
func resolveTokensFile(configPath, tokensFile string) (string, error) {
	if tokensFile != "" {
		return tokensFile, nil
	}
	if configPath == "" {
		return "", errors.New("either --config or --tokens-file is required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	if cfg.Auth.Bearer.TokensFile == "" {
		return "", errors.New("config auth.bearer.tokens_file is empty; use --tokens-file instead")
	}
	return cfg.Auth.Bearer.TokensFile, nil
}

// ─────────────────── create ───────────────────

func runTokenCreate(args []string) error {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	label := fs.String("label", "", "human-readable label for the token (required)")
	id := fs.String("id", "", "custom id (default: derived from label)")
	configPath, tokensFile := tokenFlags(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *label == "" {
		return errors.New("--label is required")
	}

	path, err := resolveTokensFile(*configPath, *tokensFile)
	if err != nil {
		return err
	}

	records, err := readTokens(path)
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

	tok, err := auth.GenerateToken()
	if err != nil {
		return err
	}

	rec := auth.TokenRecord{
		ID:    idVal,
		Token: tok,
		Label: *label,
		Metadata: map[string]any{
			"created_at": time.Now().UTC().Format(time.RFC3339),
		},
	}
	records = append(records, rec)

	if err := writeTokens(path, records); err != nil {
		return err
	}

	fmt.Printf("created token\n")
	fmt.Printf("  id:    %s\n", idVal)
	fmt.Printf("  label: %s\n", *label)
	fmt.Printf("  token: %s\n", tok)
	fmt.Printf("\nRecord this token now — it will not be shown again.\n")
	return nil
}

// ─────────────────── list ───────────────────

func runTokenList(args []string) error {
	fs := flag.NewFlagSet("token ls", flag.ContinueOnError)
	configPath, tokensFile := tokenFlags(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := resolveTokensFile(*configPath, *tokensFile)
	if err != nil {
		return err
	}
	records, err := readTokens(path)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		fmt.Println("no tokens configured")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLABEL\tTOKEN\tCREATED")
	for _, r := range records {
		created := ""
		if r.Metadata != nil {
			if c, ok := r.Metadata["created_at"].(string); ok {
				created = c
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, r.Label, maskToken(r.Token), created)
	}
	return tw.Flush()
}

// ─────────────────── revoke ───────────────────

func runTokenRevoke(args []string) error {
	fs := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	configPath, tokensFile := tokenFlags(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: pagefault token revoke <id>")
	}
	target := fs.Arg(0)

	path, err := resolveTokensFile(*configPath, *tokensFile)
	if err != nil {
		return err
	}
	records, err := readTokens(path)
	if err != nil {
		return err
	}

	out := make([]auth.TokenRecord, 0, len(records))
	found := false
	for _, r := range records {
		if r.ID == target {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return fmt.Errorf("token %q not found", target)
	}
	if err := writeTokens(path, out); err != nil {
		return err
	}
	fmt.Printf("revoked token %q\n", target)
	return nil
}

// ─────────────────── helpers ───────────────────

// readTokens reads the JSONL tokens file. A missing file is treated as
// empty; any other error is returned.
func readTokens(path string) ([]auth.TokenRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return auth.ParseTokensJSONL(data)
}

// writeTokens writes records atomically via a temp file + rename. Creates
// any missing parent directories.
func writeTokens(path string, records []auth.TokenRecord) error {
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

// maskToken returns a short prefix + suffix of the token, enough to
// disambiguate but not enough to reconstruct.
func maskToken(tok string) string {
	if len(tok) < 12 {
		return "***"
	}
	return tok[:6] + "…" + tok[len(tok)-4:]
}

// slugify produces a lowercase, hyphen-separated ID from a free-form label.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == ' ':
			if !lastDash && b.Len() > 0 {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
