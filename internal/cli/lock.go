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
// leaving it exclusive between processes — see withGlobalLock.
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
// It is REENTRANT WITHIN A PROCESS: a nested call for the same home runs fn
// directly instead of acquiring again. gofrs/flock is not reentrant — a second
// acquire from the same process blocks until the timeout and then reports
// "another agentsync process running?", naming a process that does not exist.
//
// This is not a theoretical nicety. The subagent migration must run under the
// lock (it renames canonical files and does a read-modify-write of
// targets.json), and it is reachable BOTH from `migrate subagents` (no lock
// held) and from the interactive offer inside apply/import/reconcile (lock
// already held). Requiring every caller to know which side it is on is exactly
// the kind of whole-call-graph audit that silently rots: the first attempt at
// this got it wrong and deadlocked all three commands on the accept branch.
// Making the primitive reentrant fixes the class.
//
// Between processes nothing changes — the flock is still held for the whole
// outermost frame, and only the outermost frame releases it.
func withGlobalLock(home string, fn func() error) error {
	lockPath := filepath.Join(home, ".state", "agentsync.lock")

	heldLocksMu.Lock()
	depth := heldLocks[lockPath]
	if depth > 0 {
		heldLocks[lockPath] = depth + 1
		heldLocksMu.Unlock()
		defer func() {
			heldLocksMu.Lock()
			heldLocks[lockPath]--
			heldLocksMu.Unlock()
		}()
		return fn()
	}
	heldLocksMu.Unlock()

	lock, err := iox.AcquireLockTimeout(lockPath, lockTimeout())
	if err != nil {
		return fmt.Errorf("acquire agentsync lock at %s: %w (another agentsync process running?)", lockPath, err)
	}
	heldLocksMu.Lock()
	heldLocks[lockPath] = 1
	heldLocksMu.Unlock()

	defer func() {
		// Drop the bookkeeping BEFORE releasing the flock, so the path is never
		// recorded as held by this process while another process could take it.
		heldLocksMu.Lock()
		delete(heldLocks, lockPath)
		heldLocksMu.Unlock()
		_ = lock.Release()
	}()
	return fn()
}

// lockedRun wraps a cobra RunE so the command body executes under the global
// lock. Used by the agent/plugin mutators that do a read-modify-write of
// agentsync.toml or plugins/<id>.toml: without serialization, two concurrent
// runs (or one racing a locked `apply`/`update`) lose an update — AtomicWrite
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
