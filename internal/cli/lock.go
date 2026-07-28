package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/paths"
)

// defaultLockTimeout is how long mutating CLI commands wait for the global lock
// before giving up with a clear error. Long enough that a slow apply against a
// large config doesn't spuriously block the next legit run, but short enough
// that a forgotten background process is obvious.
const defaultLockTimeout = 30 * time.Second

// lockTimeout returns the global-lock wait timeout. AGENTSYNC_LOCK_TIMEOUT_MS
// overrides the default (in milliseconds) for tests and unusual operator setups
// — the contention path is otherwise a 30s wait no test can afford. An unset or
// unparseable value keeps the default; a negative value is ignored.
func lockTimeout() time.Duration {
	if v := os.Getenv("AGENTSYNC_LOCK_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return defaultLockTimeout
}

// heldLocks counts, per lock path, how many withGlobalLock frames this PROCESS
// currently has open. It makes the global lock reentrant within a process while
// leaving it exclusive between processes.
//
// Keyed by path with no owner identity — read withGlobalLock's PRECONDITION
// before relying on it, especially from a test.
var (
	heldLocksMu sync.Mutex
	heldLocks   = map[string]int{}
)

// withGlobalLock acquires the agentsync global lock and runs fn. It must
// wrap any command that mutates ~/.agentsync/, native agent destinations,
// or .state/targets.json. Concurrent runs without this serialization can
// corrupt targets.json (read-modify-write race) and produce ghost-orphan
// state entries.
//
// The lock file lives at <home>/.state/agentsync.lock. gofrs/flock creates it
// 0o600 (owner-only) and it is re-used across runs.
//
// It is REENTRANT: a call made while this process already holds the lock for
// the same home runs fn directly instead of acquiring again. gofrs/flock is not
// reentrant — a second acquire from the same process blocks until the timeout
// and then reports "another agentsync process running?", naming a process that
// does not exist.
//
// This is not a theoretical nicety. The subagent migration must run under the
// lock (it renames canonical files and does a read-modify-write of
// targets.json), and it is reachable BOTH from `migrate subagents` (no lock
// held) and from the interactive offer inside apply/import/reconcile (lock
// already held). Requiring every caller to know which side it is on is exactly
// the kind of whole-call-graph audit that silently rots — the first attempt at
// this got it wrong and deadlocked several commands on the accept branch, and
// two later attempts to enumerate exactly which were each wrong in a different
// direction. Hence: reentrant primitive, no caller list to keep true.
//
// PRECONDITION, and the honest limit of the mechanism: the counter is keyed by
// lock PATH ONLY, with no owner identity. It therefore means "some frame in
// this PROCESS holds it", not "an enclosing frame on this goroutine holds it" —
// Go exposes no goroutine identity to key on. So reentrancy is safe only for
// STRICTLY STACK-NESTED calls on ONE goroutine, which is what the CLI does: one
// command per process, every call site a cobra RunE.
//
// The consequence to know about is in TESTS, which run commands in-process: two
// CONCURRENT goroutines calling withGlobalLock for the same home would see
// depth > 0 and both skip the flock, so a test written that way would silently
// pass while proving nothing about serialization. A test that needs real
// contention must seed the lock out-of-band with iox.AcquireLock (as the
// existing contention tests do) or spawn a separate process.
//
// Between processes nothing changes — the flock is held for the whole outermost
// frame, and only the outermost frame releases it.
func withGlobalLock(home string, fn func() error) error {
	lockPath := filepath.Join(home, ".state", "agentsync.lock")

	heldLocksMu.Lock()
	if heldLocks[lockPath] > 0 {
		heldLocks[lockPath]++
		heldLocksMu.Unlock()
		defer releaseHeld(lockPath)
		return fn()
	}
	heldLocksMu.Unlock()

	lock, err := iox.AcquireLockTimeout(lockPath, lockTimeout())
	if err != nil {
		return fmt.Errorf("acquire agentsync lock at %s: %w (another agentsync process running?)", lockPath, err)
	}
	heldLocksMu.Lock()
	// Increment rather than assign 1: if another goroutine raced past the
	// depth check above and is inside its own frame, assigning would erase its
	// claim and let this frame's releaseHeld delete the entry out from under it.
	// (That race is outside the documented precondition, but the counter should
	// not be the thing that makes it worse.)
	heldLocks[lockPath]++
	heldLocksMu.Unlock()

	defer func() {
		// Release the flock, then drop the bookkeeping. Both orders converge on
		// the same end state (Release's error is discarded and releaseHeld runs
		// either way), so this is ordering for readability, not for safety: the
		// process stops advertising the lock only once it has actually let go.
		_ = lock.Release()
		releaseHeld(lockPath)
	}()
	return fn()
}

// releaseHeld drops one frame's claim on lockPath, deleting the entry at zero so
// the map cannot accumulate keys. Decrementing uniformly (rather than deleting
// outright in the outermost frame) keeps the count correct even if frames ever
// unwind out of order, where a bare delete would strand a nested frame's
// decrement and resurrect the key as -1.
func releaseHeld(lockPath string) {
	heldLocksMu.Lock()
	defer heldLocksMu.Unlock()
	if n := heldLocks[lockPath] - 1; n > 0 {
		heldLocks[lockPath] = n
		return
	}
	delete(heldLocks, lockPath)
}

// lockedRun wraps a cobra RunE so the command body executes under the global
// lock. Used by the agent/plugin mutators that do a read-modify-write of
// agentsync.toml or plugins/<id>.toml: without serialization, two concurrent
// runs (or one racing a locked `apply`/`plugin upgrade`) lose an update — AtomicWrite
// prevents a torn file but not a stale-read overwrite.
//
// The wrapped function MAY acquire the global lock itself: withGlobalLock is
// reentrant within a process, so a nested acquire for the same home is a no-op
// rather than the self-deadlock it used to be.
func lockedRun(fn func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		home := paths.AgentsyncHome(paths.OSEnv{})
		return withGlobalLock(home, func() error {
			return fn(cmd, args)
		})
	}
}
