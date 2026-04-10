// Package write implements pagefault's Phase-4 writeback primitives.
//
// A [Writer] is the mutation side of a filesystem-like backend. It
// understands file paths (absolute, pre-sandboxed by the caller), not
// URIs — URI → path translation and write_paths enforcement live in the
// backend so this package stays a small, testable primitive layer.
//
// The filesystem implementation serializes mutations through a POSIX
// advisory lock ([flock(2)]) when LockMode is [LockFlock]. The lock is
// taken on the open file descriptor (not the inode) so concurrent
// writers in the same process cooperate, and external writers that also
// honor flock (editors, the openclaw CLI, etc.) do too. A lockMode of
// [LockNone] falls back to a per-writer [sync.Mutex] as the only
// concurrency guard; it is intended for test environments and
// single-writer deployments.
//
// Every public method honours [context.Context] cancellation and
// returns a wrapped error describing the operation that failed — call
// sites in the dispatcher re-wrap these with the pagefault sentinel
// errors so the REST envelope carries the right code/status.
//
// [flock(2)]: https://man7.org/linux/man-pages/man2/flock.2.html
package write

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// WriteMode names the mutation policy a writable backend advertises.
//
// [WriteModeAppend] is the safe default — the tool layer rejects any
// write that would bypass the entry-template wrapper. [WriteModeAny]
// is an opt-in that unlocks pf_poke's format:"raw", which hands the
// caller's bytes to the backend without a timestamped header.
//
// As of 0.5.1 the [Writer] interface only exposes [Writer.Append], so
// neither mode actually performs prepends or overwrites — "any" is
// purely a gate for raw format. Prepend and overwrite are planned
// follow-ups; until they ship, the mode's only observable effect is
// the format:"raw" unlock.
type WriteMode string

const (
	// WriteModeAppend only permits Append operations. Default.
	WriteModeAppend WriteMode = "append"
	// WriteModeAny is a second-tier operator opt-in. Its only
	// current effect is unlocking format:"raw" in pf_poke
	// (bypassing the entry-template wrapper); prepend/overwrite
	// operations are reserved but not yet implemented.
	WriteModeAny WriteMode = "any"
)

// LockMode selects the file-locking strategy for a FilesystemWriter.
//
// [LockFlock] uses a POSIX advisory lock (LOCK_EX) around every
// mutation — this cooperates with other flock-aware writers on the
// same machine (editors, other processes). [LockNone] skips the flock
// call entirely and relies on the writer's per-process mutex only;
// use it when pagefault is the sole writer for the path.
type LockMode string

const (
	// LockFlock is the default: LOCK_EX via flock(2).
	LockFlock LockMode = "flock"
	// LockNone disables advisory locking. Single-writer environments only.
	LockNone LockMode = "none"
)

// Writer is the mutation surface shared by every filesystem-like
// backend. Implementations must be safe for concurrent use.
//
// All paths are absolute; callers are expected to have already
// sandboxed the path to the backend's root. The writer does not
// interpret URIs — that's the backend's job.
type Writer interface {
	// Append writes content to the end of the file at absPath, creating
	// parent directories and the file itself if necessary. Returns the
	// number of bytes written on success.
	Append(ctx context.Context, absPath, content string) (int, error)
}

// FilesystemWriter is the filesystem implementation of [Writer]. Zero
// value is not usable — construct via [NewFilesystemWriter].
type FilesystemWriter struct {
	lockMode LockMode
	// mu serialises writes within a single process when flock is
	// unavailable (LockNone) or fails. Even with flock we hold the
	// mutex while the file is open so concurrent append calls don't
	// race inside pagefault itself.
	mu sync.Mutex
}

// NewFilesystemWriter returns a writer that uses the given lock mode.
// An empty mode is interpreted as [LockFlock].
func NewFilesystemWriter(mode LockMode) *FilesystemWriter {
	if mode == "" {
		mode = LockFlock
	}
	return &FilesystemWriter{lockMode: mode}
}

// Append opens absPath with O_APPEND|O_WRONLY|O_CREATE and writes
// content. A parent directory is created if missing. Flock is taken on
// the open fd for the duration of the write when LockMode is
// [LockFlock]; the fd is always closed before Append returns.
//
// The returned int is the number of bytes written. On partial-write
// failure (write returns fewer bytes than len(content)) Append returns
// a wrapped error and the partial byte count.
func (w *FilesystemWriter) Append(ctx context.Context, absPath, content string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if absPath == "" {
		return 0, fmt.Errorf("write: empty path")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return 0, fmt.Errorf("write: mkdir %q: %w", filepath.Dir(absPath), err)
	}

	f, err := os.OpenFile(absPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return 0, fmt.Errorf("write: open %q: %w", absPath, err)
	}
	defer f.Close()

	if w.lockMode == LockFlock {
		if err := lockFile(f); err != nil {
			return 0, fmt.Errorf("write: lock %q: %w", absPath, err)
		}
		defer func() { _ = unlockFile(f) }()
	}

	n, err := f.WriteString(content)
	if err != nil {
		return n, fmt.Errorf("write: append %q: %w", absPath, err)
	}
	if n != len(content) {
		return n, fmt.Errorf("write: short write on %q: %d/%d bytes", absPath, n, len(content))
	}
	if err := f.Sync(); err != nil {
		return n, fmt.Errorf("write: sync %q: %w", absPath, err)
	}
	return n, nil
}

// lockFile takes a POSIX advisory exclusive lock on f via flock(2).
// The lock is associated with the open file descriptor and released
// when the fd is closed (or by explicit unlock).
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile releases an advisory lock taken by lockFile.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
