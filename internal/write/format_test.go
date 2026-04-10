package write

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedClock returns a deterministic time so the entry header is
// predictable across runs.
func fixedClock(hour, minute int) Clock {
	t := time.Date(2026, 4, 11, hour, minute, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func TestFormatEntry_DefaultIsEntry(t *testing.T) {
	got, err := FormatEntry("hello", "", "Claude Code", fixedClock(23, 15))
	require.NoError(t, err)
	assert.Contains(t, got, "## [23:15] via pagefault (Claude Code)")
	assert.Contains(t, got, "hello")
	assert.True(t, strings.HasPrefix(got, "\n---\n"), "entry should start with newline + rule")
	assert.True(t, strings.HasSuffix(got, "\n"), "entry should end with newline")
}

func TestFormatEntry_EntryExplicit(t *testing.T) {
	got, err := FormatEntry("note body", EntryFormatEntry, "cli", fixedClock(9, 5))
	require.NoError(t, err)
	assert.Contains(t, got, "## [09:05] via pagefault (cli)")
	assert.Contains(t, got, "note body")
}

func TestFormatEntry_EntryNoLabelStripsSuffix(t *testing.T) {
	got, err := FormatEntry("content", EntryFormatEntry, "", fixedClock(12, 0))
	require.NoError(t, err)
	assert.Contains(t, got, "## [12:00] via pagefault\n")
	assert.NotContains(t, got, "via pagefault ()", "empty label should not produce empty parens")
}

func TestFormatEntry_EntryAddsTrailingNewline(t *testing.T) {
	got, err := FormatEntry("no newline", EntryFormatEntry, "cli", fixedClock(0, 0))
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(got, "no newline\n"),
		"entry body should end in newline even if the input doesn't: got %q", got)
}

func TestFormatEntry_EntryPreservesExistingNewline(t *testing.T) {
	got, err := FormatEntry("already has newline\n", EntryFormatEntry, "cli", fixedClock(0, 0))
	require.NoError(t, err)
	// Should not double the newline.
	assert.True(t, strings.HasSuffix(got, "already has newline\n"))
	assert.False(t, strings.HasSuffix(got, "already has newline\n\n"))
}

func TestFormatEntry_Raw(t *testing.T) {
	got, err := FormatEntry("raw bytes", EntryFormatRaw, "cli", fixedClock(0, 0))
	require.NoError(t, err)
	assert.Equal(t, "raw bytes", got, "raw mode should be identity")
}

func TestFormatEntry_UnknownFormat(t *testing.T) {
	_, err := FormatEntry("x", "nope", "cli", fixedClock(0, 0))
	require.Error(t, err)
}

func TestFormatEntry_NilClockUsesDefault(t *testing.T) {
	// Just verify it doesn't panic and produces something. We don't
	// assert the exact timestamp because DefaultClock is wall time.
	got, err := FormatEntry("x", EntryFormatEntry, "cli", nil)
	require.NoError(t, err)
	assert.Contains(t, got, "via pagefault (cli)")
}

func TestDefaultClockIsUTC(t *testing.T) {
	got := DefaultClock()
	assert.Equal(t, time.UTC, got.Location())
}
