package tool

import (
	"context"
	"fmt"
	"time"

	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/model"
)

// DeepRetrieveInput is the request shape for pf_fault.
type DeepRetrieveInput struct {
	Query          string `json:"query"`
	Agent          string `json:"agent,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
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

	res, err := d.DeepRetrieve(ctx, in.Query, in.Agent, timeout, caller)
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
