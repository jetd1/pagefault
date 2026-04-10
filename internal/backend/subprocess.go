package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

// SubprocessBackend runs an external command to answer Search requests.
// It is the generic execute-and-parse backend — the canonical use case
// is ripgrep, but any command that emits a structured match list will
// work.
//
// Read and ListResources are noops: subprocess backends exist purely to
// answer queries. If you need read access, point a filesystem backend at
// the same roots.
//
// Config contract:
//   - Command is a tokenized template (see tokenizeCommand).
//   - {query} and {roots} in any token are substituted at Search time.
//   - Parse is "ripgrep_json" (jsonl, ripgrep --json format), "grep"
//     (classic path:lineno:content lines), or empty / "plain" (each
//     stdout line becomes a snippet with an "unknown" URI).
type SubprocessBackend struct {
	name    string
	argv    []string
	roots   []string
	timeout time.Duration
	parse   string
}

// NewSubprocessBackend constructs a subprocess backend from config.
func NewSubprocessBackend(cfg *config.SubprocessBackendConfig) (*SubprocessBackend, error) {
	if cfg == nil {
		return nil, errors.New("subprocess backend: nil config")
	}
	argv, err := tokenizeCommand(cfg.Command)
	if err != nil {
		return nil, fmt.Errorf("subprocess backend %q: %w", cfg.Name, err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("subprocess backend %q: empty command template", cfg.Name)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10
	}
	parse := cfg.Parse
	switch parse {
	case "", "plain", "grep", "ripgrep_json":
		// ok
	default:
		return nil, fmt.Errorf("subprocess backend %q: unknown parse mode %q", cfg.Name, parse)
	}
	return &SubprocessBackend{
		name:    cfg.Name,
		argv:    argv,
		roots:   append([]string(nil), cfg.Roots...),
		timeout: time.Duration(timeout) * time.Second,
		parse:   parse,
	}, nil
}

// Name returns the backend name.
func (b *SubprocessBackend) Name() string { return b.name }

// Read is not supported — subprocess backends only answer Search.
func (b *SubprocessBackend) Read(ctx context.Context, uri string) (*Resource, error) {
	return nil, fmt.Errorf("%w: subprocess backend %q does not support Read", model.ErrResourceNotFound, b.name)
}

// ListResources is a noop for subprocess backends.
func (b *SubprocessBackend) ListResources(ctx context.Context) ([]ResourceInfo, error) {
	return nil, nil
}

// Search runs the configured command with {query} and {roots}
// substituted, then parses stdout according to Parse.
func (b *SubprocessBackend) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	// Substitute {query} and {roots}. If a token IS literally "{roots}"
	// we splice the slice in place; otherwise we string-substitute.
	var args []string
	for _, tok := range b.argv {
		if tok == "{roots}" {
			args = append(args, b.roots...)
			continue
		}
		tok = strings.ReplaceAll(tok, "{query}", query)
		tok = strings.ReplaceAll(tok, "{roots}", strings.Join(b.roots, " "))
		args = append(args, tok)
	}

	runCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, args[0], args[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	// Tools like grep/ripgrep exit with status 1 when there are no
	// matches. That's not an error — return an empty result.
	if err != nil && !isNoMatchExit(err) {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: subprocess %q timed out after %s",
				model.ErrBackendUnavailable, b.name, b.timeout)
		}
		stderrMsg := strings.TrimSpace(stderr.String())
		if stderrMsg != "" {
			return nil, fmt.Errorf("%w: subprocess %q: %s: %s",
				model.ErrBackendUnavailable, b.name, err.Error(), stderrMsg)
		}
		return nil, fmt.Errorf("%w: subprocess %q: %s",
			model.ErrBackendUnavailable, b.name, err.Error())
	}

	switch b.parse {
	case "ripgrep_json":
		return parseRipgrepJSON(stdout.Bytes(), limit, b.name), nil
	case "grep":
		return parseGrep(stdout.Bytes(), limit, b.name), nil
	default: // "", "plain"
		return parsePlain(stdout.Bytes(), limit, b.name), nil
	}
}

// isNoMatchExit reports whether an exec.Run error is a "no matches" exit
// — grep and ripgrep both use exit code 1 for this case.
func isNoMatchExit(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	return ee.ExitCode() == 1
}

// parseRipgrepJSON decodes ripgrep --json output (one JSON object per
// line). Only "match" events are kept; each yields one SearchResult.
func parseRipgrepJSON(raw []byte, limit int, backend string) []SearchResult {
	var out []SearchResult
	sc := bufio.NewScanner(bytes.NewReader(raw))
	// ripgrep can emit long lines; raise the buffer.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg struct {
			Type string `json:"type"`
			Data struct {
				Path       struct{ Text string } `json:"path"`
				Lines      struct{ Text string } `json:"lines"`
				LineNumber int                   `json:"line_number"`
			} `json:"data"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Type != "match" {
			continue
		}
		out = append(out, SearchResult{
			URI:     msg.Data.Path.Text,
			Snippet: strings.TrimRight(msg.Data.Lines.Text, "\n"),
			Metadata: map[string]any{
				"backend": backend,
				"line":    msg.Data.LineNumber,
			},
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// parseGrep decodes classic "path:lineno:content" grep output. Each line
// is split on the first two colons; anything else is skipped.
func parseGrep(raw []byte, limit int, backend string) []SearchResult {
	var out []SearchResult
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		// Split at most twice from the left.
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		rest := line[i+1:]
		j := strings.IndexByte(rest, ':')
		if j < 0 {
			continue
		}
		path := line[:i]
		lineno, _ := strconv.Atoi(rest[:j])
		snippet := rest[j+1:]
		out = append(out, SearchResult{
			URI:     path,
			Snippet: snippet,
			Metadata: map[string]any{
				"backend": backend,
				"line":    lineno,
			},
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// parsePlain returns each non-empty stdout line as a single result with
// URI = "unknown://<line>" and the line as the snippet. Only useful for
// quick smoke tests; operators are expected to pick a structured mode.
func parsePlain(raw []byte, limit int, backend string) []SearchResult {
	var out []SearchResult
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		out = append(out, SearchResult{
			URI:      "",
			Snippet:  line,
			Metadata: map[string]any{"backend": backend},
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}
