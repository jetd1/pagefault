package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/model"
)

// DeepRetrieveInput is the request shape for pf_fault.
//
// TimeRangeStart / TimeRangeEnd are optional free-form hints
// restricting the subagent's search to a time window. Either or both
// may be set; pagefault formats them into a single hint string and
// passes it through to the subagent via the prompt template's
// {time_range} placeholder. Values are not parsed — the subagent
// interprets them — so any human-readable form (ISO 8601,
// "last Tuesday", "Q1 2026") works.
type DeepRetrieveInput struct {
	Query          string `json:"query"`
	Agent          string `json:"agent,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	TimeRangeStart string `json:"time_range_start,omitempty"`
	TimeRangeEnd   string `json:"time_range_end,omitempty"`
}

// DeepRetrieveOutput is the response shape for pf_fault. On timeout
// TimedOut is true and PartialResult may carry whatever the subagent
// produced before the deadline.
type DeepRetrieveOutput struct {
	Answer         string  `json:"answer,omitempty"`
	Agent          string  `json:"agent"`
	Backend        string  `json:"backend"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	TimedOut       bool    `json:"timed_out,omitempty"`
	PartialResult  string  `json:"partial_result,omitempty"`
}

// defaultDeepRetrieveTimeout is used when the caller doesn't specify
// TimeoutSeconds. Matches the value documented in pf_fault's spec.
const defaultDeepRetrieveTimeout = 120 * time.Second

// HandleDeepRetrieve spawns a subagent to perform a deep retrieval. It
// is the pure, transport-agnostic body of pf_fault.
func HandleDeepRetrieve(ctx context.Context, d *dispatcher.ToolDispatcher, in DeepRetrieveInput, caller model.Caller) (DeepRetrieveOutput, error) {
	if in.Query == "" {
		return DeepRetrieveOutput{}, fmt.Errorf("%w: query is required", model.ErrInvalidRequest)
	}
	timeout := time.Duration(in.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultDeepRetrieveTimeout
	}

	res, err := d.DeepRetrieve(ctx, in.Query, in.Agent, timeout, caller, dispatcher.DeepRetrieveOptions{
		TimeRange: formatTimeRange(in.TimeRangeStart, in.TimeRangeEnd),
	})
	if err != nil {
		return DeepRetrieveOutput{}, err
	}
	return DeepRetrieveOutput{
		Answer:         res.Answer,
		Agent:          res.Agent,
		Backend:        res.Backend,
		ElapsedSeconds: res.ElapsedSeconds,
		TimedOut:       res.TimedOut,
		PartialResult:  res.PartialResult,
	}, nil
}

// formatTimeRange turns the optional start/end strings into a single
// human-readable hint the subagent can reason about. The four cases:
//
//	start + end → "2026-04-01 to 2026-04-11"
//	start only  → "from 2026-04-01 onwards"
//	end only    → "up to 2026-04-11"
//	neither     → "" (no restriction; template emits nothing)
//
// Values are not validated — the subagent interprets them. Leading
// and trailing whitespace is trimmed so accidental space in a user
// input does not produce an empty-looking but non-empty string that
// would trigger the template.
func formatTimeRange(start, end string) string {
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	switch {
	case start != "" && end != "":
		return start + " to " + end
	case start != "":
		return "from " + start + " onwards"
	case end != "":
		return "up to " + end
	default:
		return ""
	}
}
