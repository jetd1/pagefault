// Package filter implements the pagefault filter pipeline. Filters run in two
// stages: URI checks (before backend access) and content transforms (after).
//
// Built-in filters:
//
//   - PathFilter: URI glob allow/deny.
//   - TagFilter:  resource tag allow/deny.
//
// Phase 3 will add RedactionFilter (regex-based content redaction).
//
// Filters are optional. A CompositeFilter with an empty filter list (or
// constructed with filters disabled) is a pass-through — every URI, every
// resource, every byte of content is allowed.
package filter

import (
	"fmt"

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

// NewFromConfig builds a CompositeFilter from a FiltersConfig. When disabled
// it returns an empty pass-through filter.
func NewFromConfig(cfg config.FiltersConfig) (*CompositeFilter, error) {
	if !cfg.Enabled {
		return NewCompositeFilter(), nil
	}
	var filters []Filter

	if len(cfg.Path.Allow) > 0 || len(cfg.Path.Deny) > 0 {
		pf, err := NewPathFilter(cfg.Path.Allow, cfg.Path.Deny)
		if err != nil {
			return nil, err
		}
		filters = append(filters, pf)
	}

	if len(cfg.Tags.Allow) > 0 || len(cfg.Tags.Deny) > 0 {
		filters = append(filters, NewTagFilter(cfg.Tags.Allow, cfg.Tags.Deny))
	}

	return NewCompositeFilter(filters...), nil
}

// ───────────────────────── PathFilter ─────────────────────────

// PathFilter enforces URI allow/deny globs.
//
//   - allow: if non-empty, the URI must match at least one allow pattern.
//   - deny:  if any deny pattern matches, the URI is blocked.
//
// Patterns use doublestar syntax (** supported).
type PathFilter struct {
	allow []string
	deny  []string
}

// NewPathFilter validates and constructs a PathFilter. Invalid patterns
// produce an error at construction time.
func NewPathFilter(allow, deny []string) (*PathFilter, error) {
	for _, p := range append(append([]string{}, allow...), deny...) {
		if !doublestar.ValidatePattern(p) {
			return nil, fmt.Errorf("filter: invalid glob pattern %q", p)
		}
	}
	return &PathFilter{allow: allow, deny: deny}, nil
}

// AllowURI checks the allow/deny globs.
func (p *PathFilter) AllowURI(uri string, _ *model.Caller) bool {
	for _, d := range p.deny {
		if ok, _ := doublestar.Match(d, uri); ok {
			return false
		}
	}
	if len(p.allow) == 0 {
		return true
	}
	for _, a := range p.allow {
		if ok, _ := doublestar.Match(a, uri); ok {
			return true
		}
	}
	return false
}

// AllowTags is a pass-through for PathFilter.
func (*PathFilter) AllowTags(string, []string, *model.Caller) bool { return true }

// FilterContent is a pass-through for PathFilter.
func (*PathFilter) FilterContent(content string, _ string) string { return content }

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
