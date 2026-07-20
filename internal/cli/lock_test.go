package cli_test

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/iox"
)

// TestLock_ApplyContendsOnHeldLock proves the global-lock wiring on BOTH sides
// of the contention: a lock released before the wait window elapses lets the
// blocked apply proceed, and a lock held for the FULL window makes apply give up
// with the human-friendly busy/timeout error (not a hang, not a silent success).
//
// We hold the lock manually (a second in-process flock FD contends with the
// apply's own) and lower the wait via AGENTSYNC_LOCK_TIMEOUT_MS so the contention
// case fails in a fraction of a second instead of the 30s production default.
func TestLock_ApplyContendsOnHeldLock(t *testing.T) {
	tests := []struct {
		name           string
		holdFullWindow bool
	}{
		{"released before the window then succeeds", false},
		{"held the full window errors busy", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			env := map[string]string{
				"AGENTSYNC_TARGET_ROOT": tmp,
				// Short enough the held-full-window case fails fast; long enough the
				// release case reliably wins the lock after the 50ms release below.
				"AGENTSYNC_LOCK_TIMEOUT_MS": "400",
			}
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

			if tc.holdFullWindow {
				// Keep the lock held for the whole run: apply must wait out the
				// window and return the busy/timeout error.
				defer func() { _ = held.Release() }()
				out, aerr := runCLI(t, env, "apply")
				if aerr == nil {
					t.Fatalf("apply against a lock held the full window must error; out:\n%s", out)
				}
				if !strings.Contains(aerr.Error(), "busy") && !strings.Contains(aerr.Error(), "another agentsync process") {
					t.Fatalf("expected a busy/timeout lock error; got: %v", aerr)
				}
				return
			}

			// Release the lock shortly after apply starts contending; the in-flight
			// apply should then acquire it and succeed.
			done := make(chan error, 1)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, aerr := runCLI(t, env, "apply")
				done <- aerr
			}()
			time.Sleep(50 * time.Millisecond)
			_ = held.Release()
			wg.Wait()
			if aerr := <-done; aerr != nil {
				t.Fatalf("apply after the lock was released should succeed; got: %v", aerr)
			}
		})
	}
}
