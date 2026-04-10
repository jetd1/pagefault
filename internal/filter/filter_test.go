package filter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

func TestPathFilter_AllowEmptyMeansAllowAll(t *testing.T) {
	pf, err := NewPathFilter(nil, nil)
	require.NoError(t, err)
	assert.True(t, pf.AllowURI("memory://anything.md", nil))
}

func TestPathFilter_DenyBlocks(t *testing.T) {
	pf, err := NewPathFilter(nil, []string{"memory://secret/*.md"})
	require.NoError(t, err)
	assert.False(t, pf.AllowURI("memory://secret/notes.md", nil))
	assert.True(t, pf.AllowURI("memory://public/notes.md", nil))
}

func TestPathFilter_AllowAllowlist(t *testing.T) {
	pf, err := NewPathFilter([]string{"memory://public/**"}, nil)
	require.NoError(t, err)
	assert.True(t, pf.AllowURI("memory://public/a.md", nil))
	assert.True(t, pf.AllowURI("memory://public/sub/b.md", nil))
	assert.False(t, pf.AllowURI("memory://private/a.md", nil))
}

func TestPathFilter_DenyTrumpsAllow(t *testing.T) {
	pf, err := NewPathFilter([]string{"memory://**/*.md"}, []string{"memory://secrets/*.md"})
	require.NoError(t, err)
	assert.False(t, pf.AllowURI("memory://secrets/x.md", nil))
	assert.True(t, pf.AllowURI("memory://notes/x.md", nil))
}

func TestPathFilter_InvalidPattern(t *testing.T) {
	_, err := NewPathFilter([]string{"[bad"}, nil)
	require.Error(t, err)
}

func TestPathFilter_DoubleStarGlob(t *testing.T) {
	pf, err := NewPathFilter(nil, []string{"memory://**/secret.md"})
	require.NoError(t, err)
	assert.False(t, pf.AllowURI("memory://a/b/c/secret.md", nil))
	assert.True(t, pf.AllowURI("memory://a/b/c/public.md", nil))
}

func TestTagFilter_DenyBlocks(t *testing.T) {
	tf := NewTagFilter(nil, []string{"intimate"})
	assert.False(t, tf.AllowTags("memory://x", []string{"intimate", "notes"}, nil))
	assert.True(t, tf.AllowTags("memory://x", []string{"notes"}, nil))
}

func TestTagFilter_AllowlistMatch(t *testing.T) {
	tf := NewTagFilter([]string{"public"}, nil)
	assert.True(t, tf.AllowTags("memory://x", []string{"public", "docs"}, nil))
	assert.False(t, tf.AllowTags("memory://x", []string{"private"}, nil))
	// Empty tag list fails allowlist check.
	assert.False(t, tf.AllowTags("memory://x", []string{}, nil))
}

func TestTagFilter_EmptyMeansAllowAll(t *testing.T) {
	tf := NewTagFilter(nil, nil)
	assert.True(t, tf.AllowTags("memory://x", []string{"any"}, nil))
	assert.True(t, tf.AllowTags("memory://x", nil, nil))
}

func TestCompositeFilter_AllAllow(t *testing.T) {
	pf, _ := NewPathFilter(nil, []string{"memory://secrets/*"})
	tf := NewTagFilter(nil, []string{"intimate"})
	c := NewCompositeFilter(pf, tf)

	assert.True(t, c.AllowURI("memory://notes/a.md", nil))
	assert.True(t, c.AllowTags("memory://notes/a.md", []string{"daily"}, nil))
}

func TestCompositeFilter_AnyDenyBlocks(t *testing.T) {
	pf, _ := NewPathFilter(nil, []string{"memory://secrets/*"})
	tf := NewTagFilter(nil, []string{"intimate"})
	c := NewCompositeFilter(pf, tf)

	assert.False(t, c.AllowURI("memory://secrets/x.md", nil))
	assert.False(t, c.AllowTags("memory://x", []string{"intimate"}, nil))
}

func TestCompositeFilter_EmptyIsPassThrough(t *testing.T) {
	c := NewCompositeFilter()
	assert.True(t, c.AllowURI("memory://anything", nil))
	assert.True(t, c.AllowTags("memory://anything", []string{"any"}, nil))
	assert.Equal(t, "hello", c.FilterContent("hello", "memory://x"))
}

func TestNewFromConfig_Disabled(t *testing.T) {
	c, err := NewFromConfig(config.FiltersConfig{Enabled: false})
	require.NoError(t, err)
	// Pass-through behavior even with rules set (because disabled).
	assert.True(t, c.AllowURI("memory://anything", nil))
}

func TestNewFromConfig_Enabled(t *testing.T) {
	cfg := config.FiltersConfig{
		Enabled: true,
		Path: config.PathFilterConfig{
			Deny: []string{"memory://secrets/**"},
		},
		Tags: config.TagFilterConfig{
			Deny: []string{"intimate"},
		},
	}
	c, err := NewFromConfig(cfg)
	require.NoError(t, err)
	assert.False(t, c.AllowURI("memory://secrets/a.md", nil))
	assert.False(t, c.AllowTags("memory://x", []string{"intimate"}, nil))
	assert.True(t, c.AllowURI("memory://notes/a.md", nil))
}

func TestNewFromConfig_InvalidGlob(t *testing.T) {
	cfg := config.FiltersConfig{
		Enabled: true,
		Path:    config.PathFilterConfig{Deny: []string{"[bad"}},
	}
	_, err := NewFromConfig(cfg)
	require.Error(t, err)
}

func TestCompositeFilter_UsesCaller(t *testing.T) {
	// Sanity check that the caller is threaded through without panicking
	// (Phase 1 filters do not inspect the caller).
	pf, _ := NewPathFilter(nil, nil)
	c := NewCompositeFilter(pf)
	caller := &model.Caller{ID: "x", Label: "x"}
	assert.True(t, c.AllowURI("memory://a", caller))
	assert.True(t, c.AllowTags("memory://a", nil, caller))
}
