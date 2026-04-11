package filter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"jetd.one/pagefault/internal/config"
	"jetd.one/pagefault/internal/model"
)

func TestPathFilter_AllowEmptyMeansAllowAll(t *testing.T) {
	pf, err := NewPathFilter(nil, nil, nil, nil)
	require.NoError(t, err)
	assert.True(t, pf.AllowURI("memory://anything.md", nil))
}

func TestPathFilter_DenyBlocks(t *testing.T) {
	pf, err := NewPathFilter(nil, []string{"memory://secret/*.md"}, nil, nil)
	require.NoError(t, err)
	assert.False(t, pf.AllowURI("memory://secret/notes.md", nil))
	assert.True(t, pf.AllowURI("memory://public/notes.md", nil))
}

func TestPathFilter_AllowAllowlist(t *testing.T) {
	pf, err := NewPathFilter([]string{"memory://public/**"}, nil, nil, nil)
	require.NoError(t, err)
	assert.True(t, pf.AllowURI("memory://public/a.md", nil))
	assert.True(t, pf.AllowURI("memory://public/sub/b.md", nil))
	assert.False(t, pf.AllowURI("memory://private/a.md", nil))
}

func TestPathFilter_DenyTrumpsAllow(t *testing.T) {
	pf, err := NewPathFilter([]string{"memory://**/*.md"}, []string{"memory://secrets/*.md"}, nil, nil)
	require.NoError(t, err)
	assert.False(t, pf.AllowURI("memory://secrets/x.md", nil))
	assert.True(t, pf.AllowURI("memory://notes/x.md", nil))
}

func TestPathFilter_InvalidPattern(t *testing.T) {
	_, err := NewPathFilter([]string{"[bad"}, nil, nil, nil)
	require.Error(t, err)
}

func TestPathFilter_DoubleStarGlob(t *testing.T) {
	pf, err := NewPathFilter(nil, []string{"memory://**/secret.md"}, nil, nil)
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
	pf, _ := NewPathFilter(nil, []string{"memory://secrets/*"}, nil, nil)
	tf := NewTagFilter(nil, []string{"intimate"})
	c := NewCompositeFilter(pf, tf)

	assert.True(t, c.AllowURI("memory://notes/a.md", nil))
	assert.True(t, c.AllowTags("memory://notes/a.md", []string{"daily"}, nil))
}

func TestCompositeFilter_AnyDenyBlocks(t *testing.T) {
	pf, _ := NewPathFilter(nil, []string{"memory://secrets/*"}, nil, nil)
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
	pf, _ := NewPathFilter(nil, nil, nil, nil)
	c := NewCompositeFilter(pf)
	caller := &model.Caller{ID: "x", Label: "x"}
	assert.True(t, c.AllowURI("memory://a", caller))
	assert.True(t, c.AllowTags("memory://a", nil, caller))
}

func TestRedactionFilter_SimpleSubstitution(t *testing.T) {
	rf, err := NewRedactionFilter([]config.RedactionRule{
		{Pattern: `(?i)api[_-]?key\s*[:=]\s*\S+`, Replacement: "[REDACTED]"},
	})
	require.NoError(t, err)
	got := rf.FilterContent("api_key: secret123 trailing", "memory://x")
	assert.Equal(t, "[REDACTED] trailing", got)
}

func TestRedactionFilter_MultipleRulesOrdered(t *testing.T) {
	rf, err := NewRedactionFilter([]config.RedactionRule{
		{Pattern: `secret`, Replacement: "[S]"},
		{Pattern: `\[S\]\d+`, Replacement: "[MASK]"},
	})
	require.NoError(t, err)
	// First rule produces "[S]123"; second rule masks it to "[MASK]".
	assert.Equal(t, "[MASK]", rf.FilterContent("secret123", "memory://x"))
}

func TestRedactionFilter_CaptureGroupReplacement(t *testing.T) {
	rf, err := NewRedactionFilter([]config.RedactionRule{
		{Pattern: `(\w+)@example\.com`, Replacement: "$1@redacted"},
	})
	require.NoError(t, err)
	assert.Equal(t, "jet@redacted says hi", rf.FilterContent("jet@example.com says hi", "memory://x"))
}

func TestRedactionFilter_InvalidPattern(t *testing.T) {
	_, err := NewRedactionFilter([]config.RedactionRule{
		{Pattern: "[unclosed", Replacement: "x"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redaction rule 0")
}

func TestRedactionFilter_PassThroughAllowStages(t *testing.T) {
	rf, err := NewRedactionFilter(nil)
	require.NoError(t, err)
	assert.True(t, rf.AllowURI("memory://x", nil))
	assert.True(t, rf.AllowTags("memory://x", []string{"any"}, nil))
	// Zero rules leaves content untouched.
	assert.Equal(t, "hello", rf.FilterContent("hello", "memory://x"))
}

func TestNewFromConfig_Redaction(t *testing.T) {
	cfg := config.FiltersConfig{
		Enabled: true,
		Redaction: config.RedactionConfig{
			Enabled: true,
			Rules: []config.RedactionRule{
				{Pattern: `pf_[A-Za-z0-9]+`, Replacement: "[TOKEN]"},
			},
		},
	}
	c, err := NewFromConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, "Bearer [TOKEN] trailing", c.FilterContent("Bearer pf_abc123 trailing", "memory://x"))
}

func TestNewFromConfig_RedactionDisabledRulesIgnored(t *testing.T) {
	// Redaction.Enabled=false means the rules are not compiled or applied
	// even if the outer filter pipeline is enabled.
	cfg := config.FiltersConfig{
		Enabled: true,
		Redaction: config.RedactionConfig{
			Enabled: false,
			Rules: []config.RedactionRule{
				{Pattern: `pf_[A-Za-z0-9]+`, Replacement: "[TOKEN]"},
			},
		},
	}
	c, err := NewFromConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, "Bearer pf_abc123", c.FilterContent("Bearer pf_abc123", "memory://x"))
}

func TestNewFromConfig_RedactionInvalidPattern(t *testing.T) {
	cfg := config.FiltersConfig{
		Enabled: true,
		Redaction: config.RedactionConfig{
			Enabled: true,
			Rules:   []config.RedactionRule{{Pattern: "[bad", Replacement: "x"}},
		},
	}
	_, err := NewFromConfig(cfg)
	require.Error(t, err)
}

// ───────────── Phase-4 write filter tests ─────────────

func TestPathFilter_AllowWriteURI_FallsBackToReadWhenUnset(t *testing.T) {
	// No writeAllow/writeDeny → AllowWriteURI delegates to the read
	// allow/deny pair. Anything the reader can touch, the writer can.
	pf, err := NewPathFilter(
		nil,
		[]string{"memory://secrets/*.md"},
		nil, nil,
	)
	require.NoError(t, err)
	assert.True(t, pf.AllowWriteURI("memory://notes/a.md", nil))
	assert.False(t, pf.AllowWriteURI("memory://secrets/x.md", nil))
}

func TestPathFilter_AllowWriteURI_UsesWriteListWhenSet(t *testing.T) {
	// When writeAllow is set, the read allowlist is ignored for writes.
	pf, err := NewPathFilter(
		[]string{"memory://**/*.md"}, // read: every .md
		nil,
		[]string{"memory://memory/*.md"}, // write: only `memory/`
		nil,
	)
	require.NoError(t, err)
	assert.True(t, pf.AllowURI("memory://random/x.md", nil), "read should be allowed")
	assert.False(t, pf.AllowWriteURI("memory://random/x.md", nil), "write should be denied")
	assert.True(t, pf.AllowWriteURI("memory://memory/x.md", nil))
}

func TestPathFilter_AllowWriteURI_WriteDenyBlocks(t *testing.T) {
	pf, err := NewPathFilter(
		nil, nil,
		nil, []string{"memory://**/secret.md"},
	)
	require.NoError(t, err)
	assert.False(t, pf.AllowWriteURI("memory://a/b/secret.md", nil))
	assert.True(t, pf.AllowWriteURI("memory://a/b/public.md", nil))
}

func TestPathFilter_NewPathFilter_InvalidWritePattern(t *testing.T) {
	_, err := NewPathFilter(nil, nil, []string{"[bad"}, nil)
	require.Error(t, err)
}

func TestTagFilter_AllowWriteURIIsPassThrough(t *testing.T) {
	tf := NewTagFilter(nil, []string{"intimate"})
	assert.True(t, tf.AllowWriteURI("memory://anything", nil))
}

func TestRedactionFilter_AllowWriteURIIsPassThrough(t *testing.T) {
	rf, err := NewRedactionFilter(nil)
	require.NoError(t, err)
	assert.True(t, rf.AllowWriteURI("memory://anything", nil))
}

func TestCompositeFilter_AllowWriteURI_AllAllow(t *testing.T) {
	pf, _ := NewPathFilter(nil, nil, []string{"memory://notes/*.md"}, nil)
	tf := NewTagFilter(nil, []string{"intimate"})
	c := NewCompositeFilter(pf, tf)
	assert.True(t, c.AllowWriteURI("memory://notes/x.md", nil))
	assert.False(t, c.AllowWriteURI("memory://other/x.md", nil))
}

func TestCompositeFilter_AllowWriteURI_EmptyIsPassThrough(t *testing.T) {
	c := NewCompositeFilter()
	assert.True(t, c.AllowWriteURI("memory://anything", nil))
}

func TestNewFromConfig_WriteAllowCompilesPathFilter(t *testing.T) {
	cfg := config.FiltersConfig{
		Enabled: true,
		Path: config.PathFilterConfig{
			WriteAllow: []string{"memory://memory/*.md"},
		},
	}
	c, err := NewFromConfig(cfg)
	require.NoError(t, err)
	assert.True(t, c.AllowWriteURI("memory://memory/today.md", nil))
	assert.False(t, c.AllowWriteURI("memory://other.md", nil))
}
