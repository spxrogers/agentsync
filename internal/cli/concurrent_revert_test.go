package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/iox"
)

// TestConcurrentRevert_LockSerializes is the revert sibling of
// TestConcurrentApply_LockSerializes: a MUTATING revert takes the same global lock
// apply holds (revert.go wraps its run in withGlobalLock), so it can't interleave
// go-git index writes on a destination repo with a concurrent apply/revert. We model
// the contention by holding the lock externally with the same iox helper the CLI uses
// and asserting a real `revert` blocks and then surfaces a clear contention error
// rather than racing or hanging forever.
func TestConcurrentRevert_LockSerializes(t *testing.T) {
	tmp, env, _ := setupGitBackedClaude(t)

	// Hold the global lock from another goroutine; the CLI's revert path must
	// observe contention and error rather than proceed.
	lockPath := filepath.Join(tmp, ".agentsync", ".state", "agentsync.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	holder, err := iox.AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("could not seed-hold lock: %v", err)
	}
	defer func() { _ = holder.Release() }()

	// Race a real revert against the held lock. Either "busy" or "another agentsync"
	// in the error proves serialization and makes the failure obvious to a human.
	done := make(chan struct {
		out string
		err error
	}, 1)
	go func() {
		out, runErr := runCLI(t, env, "revert", "claude")
		done <- struct {
			out string
			err error
		}{out, runErr}
	}()

	// flock contention surfaces within the 30s lockTimeout; cap well above it for CI.
	select {
	case res := <-done:
		if res.err == nil {
			t.Fatalf("revert with externally-held lock should fail, got success:\n%s", res.out)
		}
		lower := strings.ToLower(res.err.Error())
		if !strings.Contains(lower, "busy") && !strings.Contains(lower, "another agentsync") {
			t.Fatalf("revert error should name contention; got: %v", res.err)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("revert never returned; lock contention not surfaced as error")
	}
}

// TestConcurrentRevert_DryRunDoesNotContend is the revert sibling of
// TestConcurrentApply_DryRunDoesNotContend: `revert --dry-run` is read-only and
// deliberately skips the global lock (revert.go returns run() directly for dry-run),
// so a long real apply/revert holding the lock does not block previews.
func TestConcurrentRevert_DryRunDoesNotContend(t *testing.T) {
	tmp, env, _ := setupGitBackedClaude(t)

	lockPath := filepath.Join(tmp, ".agentsync", ".state", "agentsync.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	holder, err := iox.AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("could not seed-hold lock: %v", err)
	}
	defer func() { _ = holder.Release() }()

	// Dry-run must finish promptly even with the lock held.
	done := make(chan error, 1)
	go func() {
		_, runErr := runCLI(t, env, "revert", "claude", "--dry-run")
		done <- runErr
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dry-run revert should succeed under contention: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dry-run revert blocked behind held lock; should not acquire it")
	}
}
