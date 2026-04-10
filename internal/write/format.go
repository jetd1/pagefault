package write

import (
	"fmt"
	"strings"
	"time"
)

// EntryFormat names the wrapping applied to a pf_poke direct-append
// payload. Keep this string-typed so the YAML config, the JSON wire
// shape, and the CLI flag are trivially interchangeable.
type EntryFormat string

const (
	// EntryFormatEntry wraps content as a timestamped entry:
	//
	//   \n---\n## [HH:MM] via pagefault (<caller>)\n\n{content}\n
	//
	// The leading newline + horizontal rule safely separates the new
	// entry from whatever the file already ends with (even if the
	// existing file has no trailing newline).
	EntryFormatEntry EntryFormat = "entry"

	// EntryFormatRaw returns content unchanged. Requires the backend's
	// WriteMode to be "any" — raw bytes have no structural guarantees,
	// so it's gated the same way as overwrite/prepend operations.
	EntryFormatRaw EntryFormat = "raw"
)

// Clock is the wall clock source used by FormatEntry. Tests can inject
// a deterministic clock by supplying a zero-argument function.
type Clock func() time.Time

// DefaultClock returns time.Now in UTC. Use this unless you need a
// fixed clock for tests.
func DefaultClock() time.Time { return time.Now().UTC() }

// FormatEntry renders content according to the requested format.
//
// For "entry" mode it emits a markdown block shaped like:
//
//	\n---\n## [HH:MM] via pagefault (<callerLabel>)\n\n<content>\n
//
// ensuring the block starts on a fresh line even if the existing file
// has no trailing newline, and that it ends with a newline so the next
// append stacks cleanly. For "raw" mode it returns content unchanged.
//
// callerLabel is the audit-log label of the caller doing the write;
// when empty the "(<label>)" suffix is omitted so we don't emit an
// unhelpful "via pagefault ()".
//
// clock is optional — nil uses [DefaultClock].
func FormatEntry(content string, format EntryFormat, callerLabel string, clock Clock) (string, error) {
	switch format {
	case "", EntryFormatEntry:
		if clock == nil {
			clock = DefaultClock
		}
		ts := clock().Format("15:04")
		var header string
		if callerLabel != "" {
			header = fmt.Sprintf("## [%s] via pagefault (%s)", ts, callerLabel)
		} else {
			header = fmt.Sprintf("## [%s] via pagefault", ts)
		}
		// Ensure the content has a trailing newline so the next append
		// starts on its own line.
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		return fmt.Sprintf("\n---\n%s\n\n%s", header, content), nil
	case EntryFormatRaw:
		return content, nil
	default:
		return "", fmt.Errorf("write: unknown entry format %q", format)
	}
}
