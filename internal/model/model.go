// Package model defines shared data types and sentinel errors used across
// pagefault packages. It has no dependencies on any other pagefault package,
// so it can be imported anywhere without creating import cycles.
package model

import "errors"

// Caller identifies the authenticated entity making a request. It is produced
// by an auth provider and attached to the request context for use by the
// dispatcher, filters, and audit logger.
type Caller struct {
	// ID is a stable identifier (token ID, header value, etc.).
	ID string `json:"id"`
	// Label is a human-readable label (e.g. "Claude Code on MacBook").
	Label string `json:"label"`
	// Metadata holds extra info from the token record or header context.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// AnonymousCaller is returned by the "none" auth provider for unauthenticated
// requests in trusted/local environments.
var AnonymousCaller = Caller{ID: "anonymous", Label: "anonymous"}

// Sentinel errors used across pagefault. Callers should use errors.Is to
// check for these rather than string comparison.
var (
	// ErrAccessViolation indicates a request was blocked by a filter (path,
	// tag, write allowlist, etc.).
	ErrAccessViolation = errors.New("access violation")

	// ErrBackendUnavailable indicates a backend is configured but cannot be
	// reached (e.g., network error, missing directory).
	ErrBackendUnavailable = errors.New("backend unavailable")

	// ErrResourceNotFound indicates the requested URI does not exist on any
	// backend.
	ErrResourceNotFound = errors.New("resource not found")

	// ErrUnauthenticated indicates a missing or invalid auth credential.
	ErrUnauthenticated = errors.New("unauthenticated")

	// ErrBackendNotFound indicates a config or request referenced an unknown
	// backend name.
	ErrBackendNotFound = errors.New("backend not found")

	// ErrContextNotFound indicates a request referenced an unknown context
	// name.
	ErrContextNotFound = errors.New("context not found")

	// ErrInvalidRequest indicates the request input was malformed or failed
	// validation.
	ErrInvalidRequest = errors.New("invalid request")

	// ErrSubagentTimeout indicates a subagent (spawned via pf_fault) did
	// not complete within the configured timeout.
	ErrSubagentTimeout = errors.New("subagent timeout")

	// ErrAgentNotFound indicates a pf_fault request named an agent that
	// is not configured on any SubagentBackend.
	ErrAgentNotFound = errors.New("agent not found")

	// ErrRateLimited indicates a request was rejected by the per-caller
	// rate limiter. The REST transport maps this to HTTP 429 and code
	// "rate_limited"; the rate-limit middleware uses this sentinel so
	// the error envelope matches the shape emitted for every other
	// rejection.
	ErrRateLimited = errors.New("rate limited")

	// ErrContentTooLarge indicates a pf_poke write payload exceeded the
	// target backend's max_entry_size. Mapped to HTTP 413 and code
	// "content_too_large". The limit is measured against the raw
	// caller-supplied content (len(in.Content)) before entry-template
	// wrapping, so format="raw" and format="entry" share the same
	// budget. The check runs in the pf_poke tool layer — the backend
	// exposes the cap via its MaxEntrySize accessor but does not
	// itself enforce it. See internal/tool/write.go's
	// handleWriteDirect for the enforcement site.
	ErrContentTooLarge = errors.New("content too large")
)
