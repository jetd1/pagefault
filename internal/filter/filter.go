// Package filter implements the pagefault filter pipeline. Filters run in two
// stages: URI checks (before backend access) and content transforms (after).
//
// Built-in filters:
//
//   - PathFilter:      URI glob allow/deny.
//   - TagFilter:       resource tag allow/deny.
//   - RedactionFilter: regex-based content masking.
//
// Filters are optional. A CompositeFilter with an empty filter list (or
// constructed with filters disabled) is a pass-through — every URI, every
// resource, every byte of content is allowed.
package filter

import (
	"fmt"
	"regexp"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

// Filter is the composable unit of the pipeline. Filters return true to allow
// and false to deny; content transforms are applied in order.
type Filter interface {
	// AllowURI is evaluated before the backend is called. Returns false to
	// block the request outright.
	AllowURI(uri string, caller *model.Caller) bool

	// AllowTags is evaluated after the backend returns a resource, with the
	// resource's tag set. Returns false to redact the resource from the
	// response.
	AllowTags(uri string, tags []string, caller *model.Caller) bool

	// FilterContent transforms resource content before it is returned to
	// the caller. Phase-1 filters use this as an identity function.
	FilterContent(content string, uri string) string

	// AllowWriteURI is the Phase-4 mutation gate. It runs before the
	// dispatcher calls WritableBackend.Write, giving filters a chance
	// to refuse a write independently of the read allowlist. Filters
	// without an opinion on writes return true (pass-through).
	AllowWriteURI(uri string, caller *model.Caller) bool
}

// CompositeFilter chains multiple filters. URI and tag checks use AND
// (every filter must allow); content transforms are applied sequentially.
//
// A CompositeFilter with no filters is a pass-through.
type CompositeFilter struct {
	filters []Filter
}

// NewCompositeFilter returns a composite of the given filters.
func NewCompositeFilter(filters ...Filter) *CompositeFilter {
	return &CompositeFilter{filters: filters}
}

// AllowURI returns true if every child filter allows the URI.
func (c *CompositeFilter) AllowURI(uri string, caller *model.Caller) bool {
	for _, f := range c.filters {
		if !f.AllowURI(uri, caller) {
			return false
		}
	}
	return true
}

// AllowTags returns true if every child filter allows the tag set.
func (c *CompositeFilter) AllowTags(uri string, tags []string, caller *model.Caller) bool {
	for _, f := range c.filters {
		if !f.AllowTags(uri, tags, caller) {
			return false
		}
	}
	return true
}

// FilterContent applies each filter's content transform in order.
func (c *CompositeFilter) FilterContent(content string, uri string) string {
	for _, f := range c.filters {
		content = f.FilterContent(content, uri)
	}
	return content
}

// AllowWriteURI returns true if every child filter allows writing to
// the URI. Filters without an opinion on writes return true, so a
// pipeline that configures only read filters is a write pass-through.
func (c *CompositeFilter) AllowWriteURI(uri string, caller *model.Caller) bool {
	for _, f := range c.filters {
		if !f.AllowWriteURI(uri, caller) {
			return false
		}
	}
	return true
}

// NewFromConfig builds a CompositeFilter from a FiltersConfig. When disabled
// it returns an empty pass-through filter.
func NewFromConfig(cfg config.FiltersConfig) (*CompositeFilter, error) {
	if !cfg.Enabled {
		return NewCompositeFilter(), nil
	}
	var filters []Filter

	if len(cfg.Path.Allow) > 0 || len(cfg.Path.Deny) > 0 ||
		len(cfg.Path.WriteAllow) > 0 || len(cfg.Path.WriteDeny) > 0 {
		pf, err := NewPathFilter(cfg.Path.Allow, cfg.Path.Deny, cfg.Path.WriteAllow, cfg.Path.WriteDeny)
		if err != nil {
			return nil, err
		}
		filters = append(filters, pf)
	}

	if len(cfg.Tags.Allow) > 0 || len(cfg.Tags.Deny) > 0 {
		filters = append(filters, NewTagFilter(cfg.Tags.Allow, cfg.Tags.Deny))
	}

	if cfg.Redaction.Enabled && len(cfg.Redaction.Rules) > 0 {
		rf, err := NewRedactionFilter(cfg.Redaction.Rules)
		if err != nil {
			return nil, err
		}
		filters = append(filters, rf)
	}

	return NewCompositeFilter(filters...), nil
}

// ───────────────────────── PathFilter ─────────────────────────

// PathFilter enforces URI allow/deny globs.
//
//   - allow: if non-empty, the URI must match at least one allow pattern.
//   - deny:  if any deny pattern matches, the URI is blocked.
//
// Phase-4 added a second layer of globs that only apply to mutations
// (pf_poke): writeAllow/writeDeny. When both pairs are empty, writes
// fall through to the read allow/deny pair — this keeps the common
// case ("the only read URI is also the only write URI") from needing
// a redundant copy of the allow list. When at least one write-glob
// is configured, writes are checked exclusively against the write
// pair and ignore allow/deny entirely — "read broadly, write narrowly".
//
// Patterns use doublestar syntax (** supported).
type PathFilter struct {
	allow      []string
	deny       []string
	writeAllow []string
	writeDeny  []string
}

// NewPathFilter validates and constructs a PathFilter. Invalid patterns
// produce an error at construction time.
func NewPathFilter(allow, deny, writeAllow, writeDeny []string) (*PathFilter, error) {
	for _, group := range [][]string{allow, deny, writeAllow, writeDeny} {
		for _, p := range group {
			if !doublestar.ValidatePattern(p) {
				return nil, fmt.Errorf("filter: invalid glob pattern %q", p)
			}
		}
	}
	return &PathFilter{
		allow:      allow,
		deny:       deny,
		writeAllow: writeAllow,
		writeDeny:  writeDeny,
	}, nil
}

// AllowURI checks the allow/deny globs.
func (p *PathFilter) AllowURI(uri string, _ *model.Caller) bool {
	return matchGlobs(uri, p.allow, p.deny)
}

// AllowTags is a pass-through for PathFilter.
func (*PathFilter) AllowTags(string, []string, *model.Caller) bool { return true }

// FilterContent is a pass-through for PathFilter.
func (*PathFilter) FilterContent(content string, _ string) string { return content }

// AllowWriteURI enforces the write-specific allowlist when configured,
// otherwise delegates to the read allow/deny pair.
func (p *PathFilter) AllowWriteURI(uri string, _ *model.Caller) bool {
	if len(p.writeAllow) == 0 && len(p.writeDeny) == 0 {
		return matchGlobs(uri, p.allow, p.deny)
	}
	return matchGlobs(uri, p.writeAllow, p.writeDeny)
}

// matchGlobs applies a deny-then-allow glob check. An empty allow list
// means "allow by default"; a non-empty list requires at least one
// match.
func matchGlobs(uri string, allow, deny []string) bool {
	for _, d := range deny {
		if ok, _ := doublestar.Match(d, uri); ok {
			return false
		}
	}
	if len(allow) == 0 {
		return true
	}
	for _, a := range allow {
		if ok, _ := doublestar.Match(a, uri); ok {
			return true
		}
	}
	return false
}

// ───────────────────────── TagFilter ─────────────────────────

// TagFilter enforces tag allow/deny sets.
//
//   - allow: if non-empty, the resource must carry at least one matching tag.
//   - deny:  if any tag matches, the resource is blocked.
type TagFilter struct {
	allow map[string]struct{}
	deny  map[string]struct{}
}

// NewTagFilter constructs a TagFilter from allow/deny tag lists.
func NewTagFilter(allow, deny []string) *TagFilter {
	toSet := func(xs []string) map[string]struct{} {
		if len(xs) == 0 {
			return nil
		}
		m := make(map[string]struct{}, len(xs))
		for _, x := range xs {
			m[x] = struct{}{}
		}
		return m
	}
	return &TagFilter{allow: toSet(allow), deny: toSet(deny)}
}

// AllowURI is a pass-through for TagFilter — tag checks need the actual tags
// which are only known after fetching.
func (*TagFilter) AllowURI(string, *model.Caller) bool { return true }

// AllowTags checks the allow/deny tag sets.
func (t *TagFilter) AllowTags(_ string, tags []string, _ *model.Caller) bool {
	if len(t.deny) > 0 {
		for _, tag := range tags {
			if _, bad := t.deny[tag]; bad {
				return false
			}
		}
	}
	if len(t.allow) == 0 {
		return true
	}
	for _, tag := range tags {
		if _, ok := t.allow[tag]; ok {
			return true
		}
	}
	return false
}

// FilterContent is a pass-through for TagFilter.
func (*TagFilter) FilterContent(content string, _ string) string { return content }

// AllowWriteURI is a pass-through for TagFilter — tag checks need the
// post-read tag set, which is not available before a write.
func (*TagFilter) AllowWriteURI(string, *model.Caller) bool { return true }

// ───────────────────────── RedactionFilter ─────────────────────────

// RedactionFilter masks content bytes that match any configured regex rule.
// It runs in the FilterContent stage — after the backend has read the
// resource and after tag/path checks have decided to let the content
// through. Rules are compiled once at construction time so a bad pattern
// fails fast at server start rather than surfacing at request time.
//
// Replacement strings use Go's [regexp.Regexp.ReplaceAllString] semantics,
// so capture groups (`$1`, `$2`, …) work inside the replacement. The
// conventional replacement for a "drop this secret" rule is a literal
// `[REDACTED]`.
type RedactionFilter struct {
	rules []redactionRule
}

type redactionRule struct {
	pattern     *regexp.Regexp
	replacement string
}

// NewRedactionFilter compiles the rules and returns a filter. An invalid
// regex pattern returns an error — construction is the cheapest place to
// catch operator typos.
func NewRedactionFilter(rules []config.RedactionRule) (*RedactionFilter, error) {
	compiled := make([]redactionRule, 0, len(rules))
	for i, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("filter: redaction rule %d: compile %q: %w", i, r.Pattern, err)
		}
		compiled = append(compiled, redactionRule{pattern: re, replacement: r.Replacement})
	}
	return &RedactionFilter{rules: compiled}, nil
}

// AllowURI is a pass-through for RedactionFilter.
func (*RedactionFilter) AllowURI(string, *model.Caller) bool { return true }

// AllowTags is a pass-through for RedactionFilter.
func (*RedactionFilter) AllowTags(string, []string, *model.Caller) bool { return true }

// AllowWriteURI is a pass-through for RedactionFilter — redaction is a
// read-side transform, it has nothing to say about write requests.
func (*RedactionFilter) AllowWriteURI(string, *model.Caller) bool { return true }

// FilterContent applies every compiled rule in order.
func (f *RedactionFilter) FilterContent(content string, _ string) string {
	for _, r := range f.rules {
		content = r.pattern.ReplaceAllString(content, r.replacement)
	}
	return content
}
