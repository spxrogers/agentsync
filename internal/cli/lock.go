package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/paths"
)

// lockTimeout is how long mutating CLI commands wait for the global lock
// before giving up with a clear error. Long enough that a slow apply
// against a large config doesn't spuriously block the next legit run, but
// short enough that a forgotten background process is obvious.
const lockTimeout = 30 * time.Second

// withGlobalLock acquires the agentsync global lock and runs fn. It must
// wrap any command that mutates ~/.agentsync/, native agent destinations,
// or .state/targets.json. Concurrent runs without this serialization can
// corrupt targets.json (read-modify-write race) and produce ghost-orphan
// state entries.
//
// The lock file lives at <home>/.state/agentsync.lock. gofrs/flock creates it
// 0o600 (owner-only) and it is re-used across runs.
func withGlobalLock(home string, fn func() error) error {
	lockPath := filepath.Join(home, ".state", "agentsync.lock")
	lock, err := iox.AcquireLockTimeout(lockPath, lockTimeout)
	if err != nil {
		return fmt.Errorf("acquire agentsync lock at %s: %w (another agentsync process running?)", lockPath, err)
	}
	defer func() { _ = lock.Release() }()
	return fn()
}

// lockedRun wraps a cobra RunE so the command body executes under the global
// lock. Used by the agent/plugin mutators that do a read-modify-write of
// agentsync.toml or plugins/<id>.toml: without serialization, two concurrent
// runs (or one racing a locked `apply`/`update`) lose an update — AtomicWrite
// prevents a torn file but not a stale-read overwrite. The wrapped function
// must NOT acquire the global lock itself (gofrs/flock would deadlock on the
// re-entrant acquire from the same process).
func lockedRun(fn func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		home := paths.AgentsyncHome(paths.OSEnv{})
		return withGlobalLock(home, func() error {
			return fn(cmd, args)
		})
	}
}
