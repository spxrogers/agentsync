package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spxrogers/agentsync/internal/iox"
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
