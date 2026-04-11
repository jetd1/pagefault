package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSubmit_HappyPath — submit a task that returns immediately, Wait
// returns the terminal Done snapshot.
func TestSubmit_HappyPath(t *testing.T) {
	m := NewManager(Config{})
	defer m.Close()

	snap, err := m.Submit(SubmitRequest{
		Agent:   "alpha",
		Backend: "cli",
		Query:   "hello",
		Timeout: 5 * time.Second,
		Run: func(ctx context.Context) (string, error) {
			return "hello from alpha", nil
		},
	})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(snap.ID, "pf_tk_"))
	assert.Equal(t, StatusRunning, snap.Status)

	// Wait for completion.
	done, err := m.Wait(context.Background(), snap.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusDone, done.Status)
	assert.Equal(t, "hello from alpha", done.Result)
	assert.Empty(t, done.Error)
	assert.NotNil(t, done.CompletedAt)
	assert.Greater(t, done.Elapsed, 0.0)
}

// TestSubmit_Failure — Run returns a non-timeout error, status becomes
// Failed and Error captures the message.
func TestSubmit_Failure(t *testing.T) {
	m := NewManager(Config{})
	defer m.Close()

	snap, err := m.Submit(SubmitRequest{
		Agent:   "alpha",
		Backend: "cli",
		Timeout: 5 * time.Second,
		Run: func(ctx context.Context) (string, error) {
			return "", errors.New("kaboom")
		},
	})
	require.NoError(t, err)

	done, err := m.Wait(context.Background(), snap.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, done.Status)
	assert.Equal(t, "kaboom", done.Error)
	assert.Empty(t, done.Result)
}

// TestSubmit_Timeout — Run returns a *TimeoutError, status becomes
// TimedOut and Result carries the partial.
func TestSubmit_Timeout(t *testing.T) {
	m := NewManager(Config{})
	defer m.Close()

	snap, err := m.Submit(SubmitRequest{
		Agent:   "alpha",
		Backend: "cli",
		Timeout: 5 * time.Second,
		Run: func(ctx context.Context) (string, error) {
			return "", &TimeoutError{Partial: "half an answer"}
		},
	})
	require.NoError(t, err)

	done, err := m.Wait(context.Background(), snap.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusTimedOut, done.Status)
	assert.Equal(t, "half an answer", done.Result)
	assert.Empty(t, done.Error)
}

// TestSubmit_DetachedFromCallerContext — the passed-in caller context
// can be cancelled and the task still runs to completion.
func TestSubmit_DetachedFromCallerContext(t *testing.T) {
	m := NewManager(Config{})
	defer m.Close()

	var started int32
	snap, err := m.Submit(SubmitRequest{
		Agent:   "alpha",
		Backend: "cli",
		Timeout: 5 * time.Second,
		Run: func(ctx context.Context) (string, error) {
			atomic.StoreInt32(&started, 1)
			select {
			case <-time.After(50 * time.Millisecond):
				return "done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})
	require.NoError(t, err)

	// Wait with a short caller ctx that fires before the task.
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err = m.Wait(waitCtx, snap.ID)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// Task should still run to completion.
	time.Sleep(100 * time.Millisecond)
	final, err := m.Get(snap.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusDone, final.Status)
	assert.Equal(t, "done", final.Result)
}

// TestSubmit_MaxConcurrent — the (MaxConcurrent+1)th submit returns
// ErrBackpressure while the first MaxConcurrent tasks are still
// running.
func TestSubmit_MaxConcurrent(t *testing.T) {
	m := NewManager(Config{MaxConcurrent: 2})
	defer m.Close()

	release := make(chan struct{})
	slow := func(ctx context.Context) (string, error) {
		select {
		case <-release:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	_, err := m.Submit(SubmitRequest{Agent: "a", Timeout: 5 * time.Second, Run: slow})
	require.NoError(t, err)
	_, err = m.Submit(SubmitRequest{Agent: "a", Timeout: 5 * time.Second, Run: slow})
	require.NoError(t, err)

	// Third submit should be rejected.
	_, err = m.Submit(SubmitRequest{Agent: "a", Timeout: 5 * time.Second, Run: slow})
	assert.ErrorIs(t, err, ErrBackpressure)

	// Release the first two so they can complete.
	close(release)
}

// TestGet_Unknown — Get on an unregistered id returns ErrTaskNotFound.
func TestGet_Unknown(t *testing.T) {
	m := NewManager(Config{})
	defer m.Close()

	_, err := m.Get("pf_tk_unknown")
	assert.ErrorIs(t, err, ErrTaskNotFound)
}

// TestWait_Unknown — Wait on an unregistered id returns ErrTaskNotFound.
func TestWait_Unknown(t *testing.T) {
	m := NewManager(Config{})
	defer m.Close()

	_, err := m.Wait(context.Background(), "pf_tk_unknown")
	assert.ErrorIs(t, err, ErrTaskNotFound)
}

// TestClose_CancelsInFlight — Close cancels the ctx of running tasks
// and waits for their goroutines to exit.
func TestClose_CancelsInFlight(t *testing.T) {
	m := NewManager(Config{})

	done := make(chan struct{})
	snap, err := m.Submit(SubmitRequest{
		Agent:   "a",
		Timeout: 10 * time.Second,
		Run: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			close(done)
			return "", ctx.Err()
		},
	})
	require.NoError(t, err)

	require.NoError(t, m.Close())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not observe cancellation within 1s")
	}

	// Subsequent Submit is rejected.
	_, err = m.Submit(SubmitRequest{Agent: "a", Timeout: time.Second, Run: func(context.Context) (string, error) { return "", nil }})
	assert.ErrorIs(t, err, ErrManagerClosed)

	// The task can still be read, with its terminal state.
	final, err := m.Get(snap.ID)
	require.NoError(t, err)
	assert.True(t, final.Status.IsTerminal())
}

// TestSweep_RemovesExpired — terminal tasks older than TTL are
// reclaimed on the next sweep.
func TestSweep_RemovesExpired(t *testing.T) {
	m := NewManager(Config{TTLSeconds: 1})
	defer m.Close()

	snap, err := m.Submit(SubmitRequest{
		Agent:   "a",
		Timeout: 5 * time.Second,
		Run:     func(context.Context) (string, error) { return "ok", nil },
	})
	require.NoError(t, err)
	_, err = m.Wait(context.Background(), snap.ID)
	require.NoError(t, err)

	// Manually age the task so we don't have to sleep the test
	// past the TTL.
	m.mu.Lock()
	past := time.Now().Add(-2 * time.Second)
	m.tasks[snap.ID].task.CompletedAt = &past
	m.mu.Unlock()

	// Next Get triggers the sweep.
	_, err = m.Get(snap.ID)
	assert.ErrorIs(t, err, ErrTaskNotFound)
	assert.Equal(t, 0, m.Stats().Total)
}

// TestSweep_KeepsRunning — running tasks are never swept even when
// they exceed the TTL.
func TestSweep_KeepsRunning(t *testing.T) {
	m := NewManager(Config{TTLSeconds: 1})
	defer m.Close()

	release := make(chan struct{})
	_, err := m.Submit(SubmitRequest{
		Agent:   "a",
		Timeout: 10 * time.Second,
		Run: func(ctx context.Context) (string, error) {
			<-release
			return "ok", nil
		},
	})
	require.NoError(t, err)

	// Force the sweep with a running task present; should be a noop.
	m.mu.Lock()
	m.sweepLocked()
	m.mu.Unlock()
	assert.Equal(t, 1, m.Stats().Total)
	assert.Equal(t, 1, m.Stats().Running)

	close(release)
}

// TestConcurrent_StressManyTasks — submit a burst of tasks and make
// sure every one reaches a terminal state with the expected result.
// Runs with -race; a sync.Mutex misuse would surface here.
func TestConcurrent_StressManyTasks(t *testing.T) {
	m := NewManager(Config{MaxConcurrent: 100})
	defer m.Close()

	const N = 50
	var wg sync.WaitGroup
	ids := make([]string, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			snap, err := m.Submit(SubmitRequest{
				Agent:   "a",
				Timeout: 5 * time.Second,
				Query:   "q",
				Run: func(ctx context.Context) (string, error) {
					return fmt.Sprintf("result-%d", i), nil
				},
			})
			if err != nil {
				t.Errorf("submit %d: %v", i, err)
				return
			}
			ids[i] = snap.ID
		}(i)
	}
	wg.Wait()

	for i, id := range ids {
		if id == "" {
			continue
		}
		done, err := m.Wait(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, StatusDone, done.Status)
		assert.Equal(t, fmt.Sprintf("result-%d", i), done.Result)
	}
}

// TestGenerateSpawnID_Prefix — the generator returns pf_sp_* with
// unique output on repeated calls.
func TestGenerateSpawnID_Prefix(t *testing.T) {
	a, err := GenerateSpawnID()
	require.NoError(t, err)
	b, err := GenerateSpawnID()
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	assert.True(t, strings.HasPrefix(a, "pf_sp_"))
}

// TestConfig_Defaults — zero values in Config fall through to the
// documented defaults.
func TestConfig_Defaults(t *testing.T) {
	var c Config
	assert.Equal(t, 10*time.Minute, c.TTL())
	assert.Equal(t, 16, c.MaxConcurrency())

	c = Config{TTLSeconds: 30, MaxConcurrent: 3}
	assert.Equal(t, 30*time.Second, c.TTL())
	assert.Equal(t, 3, c.MaxConcurrency())
}

// TestStatus_IsTerminal — the predicate returns true for terminal
// states and false for running.
func TestStatus_IsTerminal(t *testing.T) {
	assert.False(t, StatusRunning.IsTerminal())
	assert.True(t, StatusDone.IsTerminal())
	assert.True(t, StatusFailed.IsTerminal())
	assert.True(t, StatusTimedOut.IsTerminal())
}
