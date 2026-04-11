// Package task implements the in-memory async task manager behind
// pf_fault (and, by extension, pf_poke mode:agent). It exists so that
// long-running subagent spawns — openclaw agent turns, LCM deep
// retrievals, anything that takes more than a few seconds — can
// survive caller-side request timeouts without blocking the MCP
// transport.
//
// Lifecycle:
//
//  1. A caller (typically ToolDispatcher.DeepRetrieveAsync) calls
//     Submit with the agent / backend / caller metadata plus a Run
//     function that performs the actual work.
//  2. Submit assigns a pf_tk_* task id, stashes a Task entry in
//     status=running, and launches a goroutine that invokes Run on a
//     context detached from the caller's HTTP request — so cancelling
//     the HTTP request does NOT cancel the subagent.
//  3. When Run returns, the goroutine updates the Task in place with
//     status=done (or failed / timed_out), records the elapsed time,
//     and closes the done channel so Wait callers can return.
//  4. Subsequent Get or Poll calls return a snapshot of the Task.
//     Terminal tasks are reclaimed by a best-effort sweep on every
//     Submit / Get / Wait after they've sat for TTLSeconds.
//
// The manager is intentionally in-memory only for 0.10.0. Surviving a
// restart mid-spawn would require persistence + a replay story, both
// of which are meaningful extra complexity for a failure mode that is
// already well-defined (client polls an unknown id, gets 404, retries
// the pf_fault). Document-and-defer.
package task

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Status is the lifecycle state of a Task.
type Status string

const (
	// StatusRunning means the goroutine is still executing Run.
	StatusRunning Status = "running"
	// StatusDone means Run returned a value with no error.
	StatusDone Status = "done"
	// StatusFailed means Run returned a non-timeout error. The
	// message is captured in Task.Error.
	StatusFailed Status = "failed"
	// StatusTimedOut means Run returned a *TimeoutError. The
	// partial result (if any) is captured in Task.Result so
	// callers can surface what the subagent produced before the
	// deadline.
	StatusTimedOut Status = "timed_out"
)

// IsTerminal reports whether a Status is a terminal state (anything
// other than StatusRunning).
func (s Status) IsTerminal() bool {
	return s != StatusRunning
}

// ErrTaskNotFound is returned by Get and Wait when the task id is
// unknown — either because it was never submitted, it already aged
// out past the TTL, or the manager was closed.
var ErrTaskNotFound = errors.New("task: not found")

// ErrBackpressure is returned by Submit when the running task count
// is already at max_concurrent. Callers should translate this into
// model.ErrRateLimited at the dispatcher layer so HTTP clients see a
// 429 instead of an opaque 500.
var ErrBackpressure = errors.New("task: max_concurrent reached")

// ErrManagerClosed is returned by Submit when the manager has been
// closed (typically during server shutdown).
var ErrManagerClosed = errors.New("task: manager closed")

// TimeoutError is the sentinel a Run function returns to signal that
// the subagent exceeded its deadline. Partial carries whatever the
// subagent produced before the timeout fired so pf_ps callers can
// still read the incomplete output.
type TimeoutError struct {
	Partial string
}

// Error satisfies the error interface.
func (e *TimeoutError) Error() string { return "task: timed out" }

// Task is a point-in-time snapshot of one tracked task. Get and Wait
// return *copies* of the internal entry — callers must not mutate
// the returned value, and consuming a stale snapshot is safe.
type Task struct {
	// ID is the pf_tk_* task identifier returned to the caller.
	ID string `json:"task_id"`
	// Status is the lifecycle state at the time of the snapshot.
	Status Status `json:"status"`
	// Agent is the subagent id the task is running.
	Agent string `json:"agent,omitempty"`
	// Backend is the subagent backend name (for disambiguation
	// when multiple subagent backends expose the same agent id).
	Backend string `json:"backend,omitempty"`
	// CallerID is the authenticated caller that initiated the
	// task. Included for audit correlation.
	CallerID string `json:"caller_id,omitempty"`
	// SpawnID is the pf_sp_* random token the dispatcher generated
	// for this call. Passed through to the subagent via the
	// {spawn_id} placeholder in the backend command template so
	// external session stores can key on it.
	SpawnID string `json:"spawn_id,omitempty"`
	// Query is the original caller query (or write content). Kept
	// alongside the result so pf_ps poll responses are
	// self-contained.
	Query string `json:"query,omitempty"`
	// CreatedAt is when Submit was called.
	CreatedAt time.Time `json:"created_at"`
	// CompletedAt is when the Run goroutine finished; nil while
	// the task is still running.
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// Elapsed is the wall-clock duration from CreatedAt to
	// CompletedAt, in fractional seconds. Zero while running.
	Elapsed float64 `json:"elapsed_seconds,omitempty"`
	// Result is the final string the Run function returned (or the
	// partial produced before a TimeoutError).
	Result string `json:"result,omitempty"`
	// Error is the stringified non-timeout error from Run, if any.
	Error string `json:"error,omitempty"`
}

// Config configures the task manager. Zero values fall through to
// sensible defaults (TTL=600s, MaxConcurrent=16).
type Config struct {
	// TTLSeconds is how long a terminal task is kept before the
	// sweep reclaims it. Zero uses 600.
	TTLSeconds int
	// MaxConcurrent caps the number of running task goroutines.
	// Zero uses 16. Submit returns ErrBackpressure when the cap
	// is reached.
	MaxConcurrent int
}

// TTL returns the configured TTL as a time.Duration, defaulting to
// 10 minutes when unset.
func (c Config) TTL() time.Duration {
	if c.TTLSeconds <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(c.TTLSeconds) * time.Second
}

// MaxConcurrency returns the configured concurrency cap, defaulting
// to 16 when unset.
func (c Config) MaxConcurrency() int {
	if c.MaxConcurrent <= 0 {
		return 16
	}
	return c.MaxConcurrent
}

// entry is the internal bookkeeping record. Unlike Task it carries a
// cancel func and done channel so Close can cancel in-flight work and
// Wait can block on completion.
type entry struct {
	task   Task
	cancel context.CancelFunc
	done   chan struct{}
}

// Manager tracks in-flight and recently-completed tasks. Safe for
// concurrent use by any number of HTTP handlers.
type Manager struct {
	cfg Config

	mu      sync.Mutex
	tasks   map[string]*entry
	running int
	closed  bool
	wg      sync.WaitGroup
}

// NewManager constructs a Manager with the given config.
func NewManager(cfg Config) *Manager {
	return &Manager{
		cfg:   cfg,
		tasks: make(map[string]*entry),
	}
}

// SubmitRequest bundles the metadata and work function for a new
// task. Run is invoked on a goroutine with a context bounded by
// Timeout; its string return becomes the Task.Result and a non-nil
// error becomes Task.Error (or StatusTimedOut + partial for a
// *TimeoutError).
type SubmitRequest struct {
	Agent    string
	Backend  string
	CallerID string
	SpawnID  string
	Query    string
	Timeout  time.Duration
	Run      func(ctx context.Context) (string, error)
}

// Submit starts a new task and returns a snapshot with
// Status=Running. The Run function is invoked on a background
// goroutine with a context detached from any caller — so cancelling
// the HTTP request does NOT cancel the spawn. Call Wait or Get to
// retrieve the final state.
func (m *Manager) Submit(req SubmitRequest) (*Task, error) {
	if req.Run == nil {
		return nil, errors.New("task: Submit: Run is required")
	}
	if req.Timeout <= 0 {
		return nil, errors.New("task: Submit: Timeout must be positive")
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrManagerClosed
	}
	if m.running >= m.cfg.MaxConcurrency() {
		m.mu.Unlock()
		return nil, ErrBackpressure
	}

	id, err := generateTaskID()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}

	// Detach from any caller context — the task outlives the
	// request. Background context plus WithTimeout means a caller
	// HTTP disconnect does not cancel the subagent.
	runCtx, runCancel := context.WithTimeout(context.Background(), req.Timeout)

	now := time.Now().UTC()
	e := &entry{
		task: Task{
			ID:        id,
			Status:    StatusRunning,
			Agent:     req.Agent,
			Backend:   req.Backend,
			CallerID:  req.CallerID,
			SpawnID:   req.SpawnID,
			Query:     req.Query,
			CreatedAt: now,
		},
		cancel: runCancel,
		done:   make(chan struct{}),
	}
	m.tasks[id] = e
	m.running++
	m.sweepLocked()
	snapshot := e.task
	m.mu.Unlock()

	m.wg.Add(1)
	go m.run(e, runCtx, runCancel, req)

	return &snapshot, nil
}

// run is the goroutine body for a submitted task. It invokes req.Run
// against the detached ctx, updates the entry with the terminal
// status, closes the done channel, and decrements the running
// counter.
//
// A panic from req.Run is converted into a StatusFailed task with
// Error="task: subagent panic: <value>". Without the recover, a
// panic here would escape an unhandled goroutine and crash the
// whole pagefault binary — Go's net/http panic recovery only
// catches panics inside request handler goroutines, and run() is
// detached from the HTTP call stack on purpose. Every backend
// Spawn method is user-facing code we do not control, so treat
// panics as the normal "subagent failed" path instead of a
// process-level fatal.
func (m *Manager) run(e *entry, ctx context.Context, cancel context.CancelFunc, req SubmitRequest) {
	defer m.wg.Done()
	defer cancel()

	var result string
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("task: subagent panic: %v", r)
			}
		}()
		result, err = req.Run(ctx)
	}()
	completed := time.Now().UTC()

	m.mu.Lock()
	e.task.CompletedAt = &completed
	e.task.Elapsed = completed.Sub(e.task.CreatedAt).Seconds()
	switch {
	case err == nil:
		e.task.Status = StatusDone
		e.task.Result = result
	default:
		var te *TimeoutError
		if errors.As(err, &te) {
			e.task.Status = StatusTimedOut
			e.task.Result = te.Partial
		} else {
			e.task.Status = StatusFailed
			e.task.Error = err.Error()
		}
	}
	m.running--
	close(e.done)
	m.mu.Unlock()
}

// Get returns a snapshot of the task with the given id. Returns
// ErrTaskNotFound if unknown. Triggers a TTL sweep as a side effect
// so long-idle deployments do not accumulate stale entries.
func (m *Manager) Get(id string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	e, ok := m.tasks[id]
	if !ok {
		return nil, ErrTaskNotFound
	}
	snapshot := e.task
	return &snapshot, nil
}

// Wait blocks until the task reaches a terminal state, the caller
// context fires, or the manager is closed. Returns the terminal
// snapshot on completion, ctx.Err() on cancellation, or
// ErrTaskNotFound / ErrManagerClosed otherwise.
//
// IMPORTANT: if ctx fires before the task completes, the task keeps
// running in the background. Subsequent Get calls will eventually
// return the final state. This is the whole point of the async
// model — caller disconnect does not cancel the subagent.
//
// On the done path we snapshot e.task directly rather than calling
// Get, because Get triggers a TTL sweep and with a sub-second TTL
// the just-finished entry could be reclaimed between the done
// signal and the map lookup. Holding a reference to `e` keeps the
// entry alive for the read even after a concurrent sweep removes
// it from the map.
func (m *Manager) Wait(ctx context.Context, id string) (*Task, error) {
	m.mu.Lock()
	e, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		return nil, ErrTaskNotFound
	}
	select {
	case <-e.done:
		m.mu.Lock()
		defer m.mu.Unlock()
		snapshot := e.task
		return &snapshot, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close cancels every in-flight task and waits for the goroutines to
// return. Subsequent Submit calls return ErrManagerClosed.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	for _, e := range m.tasks {
		if e.task.Status == StatusRunning && e.cancel != nil {
			e.cancel()
		}
	}
	m.mu.Unlock()
	m.wg.Wait()
	return nil
}

// Stats is the point-in-time counter view used by tests and /health.
type Stats struct {
	// Running is the current number of tasks in StatusRunning.
	Running int
	// Total is the total number of tracked tasks (running + terminal
	// within the TTL window).
	Total int
}

// Stats returns a snapshot of the manager counters.
func (m *Manager) Stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Stats{
		Running: m.running,
		Total:   len(m.tasks),
	}
}

// sweepLocked reclaims terminal tasks older than TTLSeconds. Caller
// must hold m.mu.
func (m *Manager) sweepLocked() {
	if len(m.tasks) == 0 {
		return
	}
	cutoff := time.Now().Add(-m.cfg.TTL())
	for id, e := range m.tasks {
		if e.task.Status == StatusRunning {
			continue
		}
		if e.task.CompletedAt != nil && e.task.CompletedAt.Before(cutoff) {
			delete(m.tasks, id)
		}
	}
}

// generateTaskID returns a cryptographically random 128-bit task id
// with a "pf_tk_" prefix.
func generateTaskID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("task: generate id: %w", err)
	}
	return "pf_tk_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// GenerateSpawnID returns a cryptographically random 128-bit token
// with a "pf_sp_" prefix, suitable for SpawnRequest.SpawnID. Exported
// so the dispatcher can mint one per pf_fault / pf_poke mode:agent
// call without reaching into the task manager's internals.
//
// Why 128 bits: we want enough entropy to act as an opaque session
// key for external stores (openclaw's gateway, etc.) without
// collisions. 128 bits is the same budget used for DCR client ids.
func GenerateSpawnID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("task: generate spawn id: %w", err)
	}
	return "pf_sp_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
