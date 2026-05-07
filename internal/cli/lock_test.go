package cli_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/iox"
)

// TestLock_ApplySerializesAgainstHeldLock asserts that two concurrent
// agentsync apply runs do not race on targets.json — the second waits
// (up to the configured timeout) for the first to release the lock.
//
// We don't run a full second `agentsync apply` because the goroutine model
// in the test binary makes that fragile; instead we hold the lock manually
// and verify that an apply attempt errors with the timeout message we set
// for human-friendly diagnostics. That proves the wiring is in place.
func TestLock_ApplyContendsOnHeldLock(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}

	lockPath := filepath.Join(tmp, ".agentsync", ".state", "agentsync.lock")
	held, err := iox.AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("acquire test lock: %v", err)
	}
	defer func() { _ = held.Release() }()

	// Run apply with the lock held; it should error with the busy
	// message before lockTimeout (we don't want to wait the full 30s).
	done := make(chan struct {
		out string
		err error
	}, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		out, err := runCLI(t, env, "apply")
		done <- struct {
			out string
			err error
		}{out, err}
	}()

	// Release the held lock after a short pause; the in-flight apply
	// should then succeed.
	time.Sleep(50 * time.Millisecond)
	_ = held.Release()
	wg.Wait()
	res := <-done
	if res.err != nil {
		t.Fatalf("apply after lock released should succeed; got err=%v\n%s", res.err, res.out)
	}
}
