package backend

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
	"github.com/jet/pagefault/internal/write"
)

// FilesystemBackend serves files from a local directory tree. It is the core
// Phase-1 backend and is responsible for:
//
//   - Glob include/exclude filtering (doublestar patterns)
//   - Sandbox enforcement (resolved paths must stay under root)
//   - URI scheme ↔ filesystem path mapping
//   - Auto-tagging by path pattern
//   - Line-range reads
//   - Simple substring search
//   - Directory enumeration
//
// Phase 4 added optional write support via the [WritableBackend]
// interface. Writable, WritePaths, WriteMode, MaxEntrySize, and the
// embedded writer are only populated when the config flips
// `writable: true`; read-only deployments leave them at zero value.
type FilesystemBackend struct {
	name      string
	root      string // absolute, symlink-resolved
	include   []string
	exclude   []string
	uriScheme string
	autoTag   map[string][]string
	sandbox   bool

	// writable fields (zero for read-only backends)
	writable     bool
	writePaths   []string
	writeMode    write.WriteMode
	maxEntrySize int
	writer       *write.FilesystemWriter
}

// Health reports the backend as unhealthy if the configured root has
// disappeared or is no longer a directory (e.g., unmounted volume). It
// is cheap — a single stat call — so it is safe to run on every
// /health probe.
func (b *FilesystemBackend) Health(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	info, err := os.Stat(b.root)
	if err != nil {
		return fmt.Errorf("stat root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("root %q is not a directory", b.root)
	}
	return nil
}

// NewFilesystemBackend constructs a filesystem backend from config. It
// resolves the root to an absolute path, verifies it exists and is a
// directory, and validates glob patterns.
func NewFilesystemBackend(cfg *config.FilesystemBackendConfig) (*FilesystemBackend, error) {
	if cfg == nil {
		return nil, errors.New("filesystem backend: nil config")
	}

	abs, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("filesystem backend %q: abs root: %w", cfg.Name, err)
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("filesystem backend %q: resolve root %q: %w", cfg.Name, abs, err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("filesystem backend %q: stat root: %w", cfg.Name, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("filesystem backend %q: root %q is not a directory", cfg.Name, resolved)
	}

	// Validate glob patterns at construction time.
	for _, g := range cfg.Include {
		if !doublestar.ValidatePattern(g) {
			return nil, fmt.Errorf("filesystem backend %q: invalid include pattern %q", cfg.Name, g)
		}
	}
	for _, g := range cfg.Exclude {
		if !doublestar.ValidatePattern(g) {
			return nil, fmt.Errorf("filesystem backend %q: invalid exclude pattern %q", cfg.Name, g)
		}
	}
	for pat := range cfg.AutoTag {
		if !doublestar.ValidatePattern(pat) {
			return nil, fmt.Errorf("filesystem backend %q: invalid auto_tag pattern %q", cfg.Name, pat)
		}
	}
	for _, g := range cfg.WritePaths {
		if !doublestar.ValidatePattern(g) {
			return nil, fmt.Errorf("filesystem backend %q: invalid write_paths pattern %q", cfg.Name, g)
		}
	}

	scheme := cfg.URIScheme
	if scheme == "" {
		scheme = cfg.Name
	}

	b := &FilesystemBackend{
		name:      cfg.Name,
		root:      resolved,
		include:   cfg.Include,
		exclude:   cfg.Exclude,
		uriScheme: scheme,
		autoTag:   cfg.AutoTag,
		sandbox:   cfg.Sandbox,
	}

	if cfg.Writable {
		b.writable = true
		b.writePaths = cfg.WritePaths
		b.writeMode = write.WriteMode(cfg.WriteMode)
		if b.writeMode == "" {
			b.writeMode = write.WriteModeAppend
		}
		b.maxEntrySize = cfg.MaxEntrySize
		b.writer = write.NewFilesystemWriter(write.LockMode(cfg.FileLocking))
	}

	return b, nil
}

// Name returns the configured backend name.
func (b *FilesystemBackend) Name() string { return b.name }

// Root returns the absolute, symlink-resolved root directory. Exposed for
// tests and diagnostics.
func (b *FilesystemBackend) Root() string { return b.root }

// URIScheme returns the URI scheme this backend handles.
func (b *FilesystemBackend) URIScheme() string { return b.uriScheme }

// relPathFromURI converts a backend URI into a filesystem-relative path,
// verifying the scheme matches.
//
// Example: "memory://foo/bar.md" -> "foo/bar.md".
func (b *FilesystemBackend) relPathFromURI(uri string) (string, error) {
	prefix := b.uriScheme + "://"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("%w: expected scheme %q, got %q", model.ErrInvalidRequest, b.uriScheme, uri)
	}
	rel := strings.TrimPrefix(uri, prefix)
	if rel == "" {
		return "", fmt.Errorf("%w: empty path in uri %q", model.ErrInvalidRequest, uri)
	}
	// Normalize to forward slashes for glob matching and reject absolute
	// or traversing paths.
	rel = filepath.ToSlash(rel)
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: absolute path in uri %q", model.ErrInvalidRequest, uri)
	}
	if strings.Contains(rel, "..") {
		return "", fmt.Errorf("%w: path traversal in uri %q", model.ErrAccessViolation, uri)
	}
	return rel, nil
}

// uriFromRelPath formats a backend URI for a relative path.
func (b *FilesystemBackend) uriFromRelPath(rel string) string {
	return b.uriScheme + "://" + filepath.ToSlash(rel)
}

// resolvePath converts a relative path into an absolute filesystem path,
// enforcing the sandbox: the resolved path must remain under root even after
// symlink resolution.
func (b *FilesystemBackend) resolvePath(rel string) (string, error) {
	joined := filepath.Join(b.root, filepath.FromSlash(rel))

	// Clean again to normalize.
	clean := filepath.Clean(joined)

	// Sandbox: ensure clean is still under root.
	if b.sandbox {
		if !isUnder(clean, b.root) {
			return "", fmt.Errorf("%w: path %q escapes root", model.ErrAccessViolation, rel)
		}
		// If the file exists, resolve symlinks and re-check.
		if _, err := os.Lstat(clean); err == nil {
			resolved, err := filepath.EvalSymlinks(clean)
			if err != nil {
				return "", fmt.Errorf("filesystem backend %q: resolve symlinks: %w", b.name, err)
			}
			if !isUnder(resolved, b.root) {
				return "", fmt.Errorf("%w: symlink target %q escapes root", model.ErrAccessViolation, resolved)
			}
			clean = resolved
		}
	}
	return clean, nil
}

// isUnder reports whether child is within (or equal to) parent after cleaning.
func isUnder(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..")
}

// matchesInclude reports whether rel (in forward-slash form) passes the
// include/exclude filters.
func (b *FilesystemBackend) matchesInclude(rel string) bool {
	rel = filepath.ToSlash(rel)

	// If include is empty, everything passes the include step.
	includeOK := len(b.include) == 0
	for _, pat := range b.include {
		if ok, _ := doublestar.Match(pat, rel); ok {
			includeOK = true
			break
		}
	}
	if !includeOK {
		return false
	}
	// Exclude overrides include.
	for _, pat := range b.exclude {
		if ok, _ := doublestar.Match(pat, rel); ok {
			return false
		}
	}
	return true
}

// tagsFor returns auto-assigned tags for the given relative path.
func (b *FilesystemBackend) tagsFor(rel string) []string {
	if len(b.autoTag) == 0 {
		return nil
	}
	rel = filepath.ToSlash(rel)

	// Iterate deterministically for test stability.
	pats := make([]string, 0, len(b.autoTag))
	for p := range b.autoTag {
		pats = append(pats, p)
	}
	sort.Strings(pats)

	seen := map[string]struct{}{}
	var tags []string
	for _, pat := range pats {
		if ok, _ := doublestar.Match(pat, rel); ok {
			for _, t := range b.autoTag[pat] {
				if _, dup := seen[t]; !dup {
					seen[t] = struct{}{}
					tags = append(tags, t)
				}
			}
		}
	}
	return tags
}

// Read fetches a single resource by URI. Supports an optional line-range
// slice via metadata-free public method; line ranges are handled at the tool
// layer by slicing the returned Content.
func (b *FilesystemBackend) Read(ctx context.Context, uri string) (*Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rel, err := b.relPathFromURI(uri)
	if err != nil {
		return nil, err
	}
	if !b.matchesInclude(rel) {
		return nil, fmt.Errorf("%w: %q not included", model.ErrResourceNotFound, uri)
	}

	abs, err := b.resolvePath(rel)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %q", model.ErrResourceNotFound, uri)
		}
		return nil, fmt.Errorf("filesystem backend %q: stat %q: %w", b.name, abs, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%w: %q is a directory", model.ErrInvalidRequest, uri)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("filesystem backend %q: read %q: %w", b.name, abs, err)
	}

	ct := contentTypeFor(rel)
	meta := map[string]any{
		"backend": b.name,
		"size":    info.Size(),
		"mtime":   info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
	}
	if tags := b.tagsFor(rel); len(tags) > 0 {
		meta["tags"] = tags
	}

	return &Resource{
		URI:         b.uriFromRelPath(rel),
		Content:     string(data),
		ContentType: ct,
		Metadata:    meta,
	}, nil
}

// Search performs a simple case-insensitive substring search across files
// matching the include filter. Returns up to limit results.
//
// Phase 1 uses naive file iteration — fine for thousands of small markdown
// files. A future phase may replace this with an index-backed backend type.
func (b *FilesystemBackend) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if query == "" {
		return nil, fmt.Errorf("%w: empty query", model.ErrInvalidRequest)
	}
	if limit <= 0 {
		limit = 10
	}
	needle := strings.ToLower(query)

	var results []SearchResult
	err := b.walkIncluded(ctx, func(rel string, abs string, info fs.FileInfo) error {
		if len(results) >= limit {
			return fs.SkipAll
		}
		if info.Size() > 4*1024*1024 { // 4MB safety cap per file
			return nil
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil // skip unreadable files
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if strings.Contains(strings.ToLower(line), needle) {
				snippet := snippetFor(line, query)
				results = append(results, SearchResult{
					URI:     b.uriFromRelPath(rel),
					Snippet: snippet,
					Metadata: map[string]any{
						"backend": b.name,
						"line":    lineNo,
						"tags":    b.tagsFor(rel),
					},
				})
				if len(results) >= limit {
					return fs.SkipAll
				}
				// Only first match per file (keeps results diverse).
				break
			}
		}
		return scanner.Err()
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, err
	}
	return results, nil
}

// ListResources enumerates accessible resources on this backend.
func (b *FilesystemBackend) ListResources(ctx context.Context) ([]ResourceInfo, error) {
	var out []ResourceInfo
	err := b.walkIncluded(ctx, func(rel string, abs string, info fs.FileInfo) error {
		meta := map[string]any{
			"backend": b.name,
			"size":    info.Size(),
			"mtime":   info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		}
		if tags := b.tagsFor(rel); len(tags) > 0 {
			meta["tags"] = tags
		}
		out = append(out, ResourceInfo{
			URI:      b.uriFromRelPath(rel),
			Metadata: meta,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URI < out[j].URI })
	return out, nil
}

// walkIncluded walks the backend root, calling fn for each file that passes
// the include/exclude filters. It honors context cancellation.
func (b *FilesystemBackend) walkIncluded(ctx context.Context, fn func(rel, abs string, info fs.FileInfo) error) error {
	return filepath.WalkDir(b.root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(b.root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !b.matchesInclude(rel) {
			return nil
		}
		// Sandbox: ensure no symlink escape.
		if b.sandbox {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil || !isUnder(resolved, b.root) {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if cbErr := fn(rel, path, info); cbErr != nil {
			return cbErr
		}
		return nil
	})
}

// contentTypeFor returns a reasonable content type guess from the file
// extension. Keeps Phase 1 simple — pluggable detection can come later.
func contentTypeFor(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".txt":
		return "text/plain"
	case ".html":
		return "text/html"
	case ".csv":
		return "text/csv"
	default:
		return "text/plain"
	}
}

// snippetFor trims a line to a short snippet centered on the match.
func snippetFor(line, needle string) string {
	const maxLen = 200
	if len(line) <= maxLen {
		return strings.TrimSpace(line)
	}
	lowerLine := strings.ToLower(line)
	idx := strings.Index(lowerLine, strings.ToLower(needle))
	if idx < 0 {
		return strings.TrimSpace(line[:maxLen])
	}
	start := idx - 60
	if start < 0 {
		start = 0
	}
	end := start + maxLen
	if end > len(line) {
		end = len(line)
	}
	prefix := ""
	if start > 0 {
		prefix = "..."
	}
	suffix := ""
	if end < len(line) {
		suffix = "..."
	}
	return prefix + strings.TrimSpace(line[start:end]) + suffix
}

// SliceLines returns the subset of content from the 1-indexed fromLine to
// toLine (inclusive). If fromLine <= 0 it defaults to 1; if toLine <= 0 it
// defaults to the last line. Out-of-range requests are clamped.
func SliceLines(content string, fromLine, toLine int) string {
	if fromLine <= 0 && toLine <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	if fromLine <= 0 {
		fromLine = 1
	}
	if toLine <= 0 || toLine > len(lines) {
		toLine = len(lines)
	}
	if fromLine > len(lines) {
		return ""
	}
	if fromLine > toLine {
		return ""
	}
	return strings.Join(lines[fromLine-1:toLine], "\n")
}

// ───────────────────────── write support (Phase 4) ─────────────────────────

// Writable reports whether this backend accepts Write calls. Backends
// configured with writable: false return false; the pf_poke dispatcher
// uses this to short-circuit with an access-violation error before
// touching any files.
func (b *FilesystemBackend) Writable() bool { return b.writable }

// WritePaths returns the configured write-path glob allowlist. May be
// empty, in which case every readable URI is considered writable (the
// operator's decision — the default config sets explicit patterns).
func (b *FilesystemBackend) WritePaths() []string { return b.writePaths }

// WriteMode returns the configured write mode ("append" or "any").
func (b *FilesystemBackend) WriteMode() string { return string(b.writeMode) }

// MaxEntrySize returns the per-write byte cap. Zero means "unlimited"
// but [config.FilesystemBackendConfig.applyWriteDefaults] sets a
// safe default (2000) when Writable is enabled.
func (b *FilesystemBackend) MaxEntrySize() int { return b.maxEntrySize }

// Write appends content to the file identified by uri, enforcing the
// write_paths allowlist and the include/exclude read filter (so writes
// can never create a file the backend would refuse to read back).
//
// Note: max_entry_size is enforced by the *caller* (the pf_poke tool
// layer in [internal/tool.HandleWrite]) against the raw caller
// content, BEFORE entry-template wrapping — see the
// [model.ErrContentTooLarge] docstring. The backend exposes the limit
// via [MaxEntrySize] for the tool layer to consult, but does not
// re-check it here, because by the time content arrives it has
// already been wrapped and the raw/wrapped distinction is lost.
//
// The caller is responsible for formatting content (entry template vs
// raw) — this method just writes bytes. It returns the number of
// bytes that hit disk.
func (b *FilesystemBackend) Write(ctx context.Context, uri string, content string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !b.writable {
		return 0, fmt.Errorf("%w: backend %q is read-only", model.ErrAccessViolation, b.name)
	}

	rel, err := b.relPathFromURI(uri)
	if err != nil {
		return 0, err
	}
	// Writes obey the read include/exclude filter too — a file we
	// wouldn't read should not magically appear via write.
	if !b.matchesInclude(rel) {
		return 0, fmt.Errorf("%w: %q not in include set", model.ErrAccessViolation, uri)
	}
	// Write-specific allowlist. Empty == "every readable URI", same as
	// `include` behaves on reads.
	if !b.matchesWritePaths(uri) {
		return 0, fmt.Errorf("%w: %q not in write_paths", model.ErrAccessViolation, uri)
	}

	abs, err := b.resolveWritePath(rel)
	if err != nil {
		return 0, err
	}
	if b.writer == nil {
		// Defensive — Writable is true but writer is nil should never
		// happen because NewFilesystemBackend constructs it together.
		return 0, fmt.Errorf("filesystem backend %q: writer not initialized", b.name)
	}
	return b.writer.Append(ctx, abs, content)
}

// matchesWritePaths reports whether uri passes the write_paths
// allowlist. An empty list means "no restriction beyond include".
func (b *FilesystemBackend) matchesWritePaths(uri string) bool {
	if len(b.writePaths) == 0 {
		return true
	}
	for _, pat := range b.writePaths {
		if ok, _ := doublestar.Match(pat, uri); ok {
			return true
		}
	}
	return false
}

// resolveWritePath is like resolvePath but tolerates a non-existent
// leaf (needed for file creation). It walks up the parent chain to
// find the first existing component and verifies that its
// symlink-resolved path is still under root, protecting against an
// attacker-placed parent symlink that would otherwise escape the
// sandbox on MkdirAll.
func (b *FilesystemBackend) resolveWritePath(rel string) (string, error) {
	joined := filepath.Join(b.root, filepath.FromSlash(rel))
	clean := filepath.Clean(joined)
	if b.sandbox && !isUnder(clean, b.root) {
		return "", fmt.Errorf("%w: path %q escapes root", model.ErrAccessViolation, rel)
	}
	if !b.sandbox {
		return clean, nil
	}

	// Walk up from the leaf until we find the first existing
	// component; that's the one we symlink-resolve.
	probe := clean
	for {
		if _, err := os.Lstat(probe); err == nil {
			resolved, rerr := filepath.EvalSymlinks(probe)
			if rerr != nil {
				return "", fmt.Errorf("filesystem backend %q: resolve %q: %w", b.name, probe, rerr)
			}
			if !isUnder(resolved, b.root) {
				return "", fmt.Errorf("%w: symlink %q escapes root", model.ErrAccessViolation, probe)
			}
			return clean, nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return clean, nil
		}
		probe = parent
	}
}

// Ensure FilesystemBackend satisfies the Backend interface at compile time.
var _ Backend = (*FilesystemBackend)(nil)

// Ensure FilesystemBackend also satisfies WritableBackend. Callers
// type-assert to WritableBackend; the check runs at compile time.
var _ WritableBackend = (*FilesystemBackend)(nil)
