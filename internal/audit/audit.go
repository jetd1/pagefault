// Package audit provides the pagefault audit logger.
//
// Every tool call is recorded to an audit sink: JSONL file, stdout, or nop
// (disabled). Entries include the caller, tool, sanitized arguments, duration,
// result size, and any error. Bearer tokens are never logged — only the
// caller's ID and label.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

// Entry is a single audit log line.
type Entry struct {
	Timestamp   time.Time      `json:"timestamp"`
	CallerID    string         `json:"caller_id"`
	CallerLabel string         `json:"caller_label,omitempty"`
	Tool        string         `json:"tool"`
	Args        map[string]any `json:"args,omitempty"`
	DurationMS  int64          `json:"duration_ms"`
	ResultSize  int            `json:"result_size"`
	Error       string         `json:"error,omitempty"`
}

// Logger writes audit entries. Implementations must be safe for concurrent
// use.
type Logger interface {
	Log(e Entry)
	io.Closer
}

// ───────────────── JSONL file sink ─────────────────

// JSONLLogger writes entries as JSON lines to an append-only file. Writes
// are serialized through a mutex; each write is a single line followed by \n.
type JSONLLogger struct {
	mu sync.Mutex
	w  *os.File
}

// NewJSONLLogger opens (or creates) a JSONL audit file for append-only
// writes. The parent directory must exist.
func NewJSONLLogger(path string) (*JSONLLogger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	return &JSONLLogger{w: f}, nil
}

// Log writes a single entry as a JSON line.
func (l *JSONLLogger) Log(e Entry) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(b)
	_, _ = l.w.Write([]byte("\n"))
}

// Close closes the underlying file.
func (l *JSONLLogger) Close() error {
	return l.w.Close()
}

// ───────────────── Stdout sink ─────────────────

// StdoutLogger writes entries as JSON lines to an arbitrary io.Writer
// (stdout by default).
type StdoutLogger struct {
	mu sync.Mutex
	w  io.Writer
}

// NewStdoutLogger returns a logger that writes to stdout.
func NewStdoutLogger() *StdoutLogger {
	return &StdoutLogger{w: os.Stdout}
}

// NewWriterLogger returns a logger that writes to an arbitrary writer. Used
// for tests.
func NewWriterLogger(w io.Writer) *StdoutLogger {
	return &StdoutLogger{w: w}
}

// Log writes a single entry as a JSON line to the configured writer.
func (l *StdoutLogger) Log(e Entry) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(b)
	_, _ = l.w.Write([]byte("\n"))
}

// Close is a no-op.
func (l *StdoutLogger) Close() error { return nil }

// ───────────────── Nop sink ─────────────────

// NopLogger discards all audit entries. Used when audit is disabled.
type NopLogger struct{}

// Log discards the entry.
func (NopLogger) Log(Entry) {}

// Close is a no-op.
func (NopLogger) Close() error { return nil }

// ───────────────── Factory & helpers ─────────────────

// NewFromConfig constructs a Logger from an AuditConfig.
//
// Mode precedence: explicit cfg.Mode wins; if empty, infer from cfg.Enabled
// and cfg.LogPath (matching applyDefaults in the config package).
func NewFromConfig(cfg config.AuditConfig) (Logger, error) {
	mode := cfg.Mode
	if mode == "" {
		switch {
		case !cfg.Enabled:
			mode = "off"
		case cfg.LogPath != "":
			mode = "jsonl"
		default:
			mode = "stdout"
		}
	}
	switch mode {
	case "off":
		return NopLogger{}, nil
	case "stdout":
		return NewStdoutLogger(), nil
	case "stderr":
		return NewWriterLogger(os.Stderr), nil
	case "jsonl":
		if cfg.LogPath == "" {
			return nil, fmt.Errorf("audit: jsonl mode requires log_path")
		}
		return NewJSONLLogger(cfg.LogPath)
	default:
		return nil, fmt.Errorf("audit: unknown mode %q", mode)
	}
}

// SanitizeArgs returns a copy of args with sensitive fields masked. Used by
// the dispatcher before logging.
func SanitizeArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if isSensitiveKey(k) {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = v
	}
	return out
}

// isSensitiveKey reports whether a field name should be masked in audit logs.
func isSensitiveKey(k string) bool {
	lower := strings.ToLower(k)
	for _, s := range []string{"token", "password", "secret", "api_key", "apikey", "authorization"} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// NewEntry builds an Entry for a tool call from its inputs. The dispatcher
// calls this at the end of a tool execution with the final duration/size.
func NewEntry(caller model.Caller, tool string, args map[string]any, start time.Time, resultSize int, err error) Entry {
	e := Entry{
		Timestamp:   start.UTC(),
		CallerID:    caller.ID,
		CallerLabel: caller.Label,
		Tool:        tool,
		Args:        SanitizeArgs(args),
		DurationMS:  time.Since(start).Milliseconds(),
		ResultSize:  resultSize,
	}
	if err != nil {
		e.Error = err.Error()
	}
	return e
}
