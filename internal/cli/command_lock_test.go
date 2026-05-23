package cli_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/iox"
)

// assertCommandBlocksOnLock proves a command acquires the global lock: with
// the lock held externally it must NOT complete (it blocks on acquisition),
// and once released it must finish cleanly. This is the regression for the
// lost-update race where marketplace add/remove and agent disable --purge
// did a load-modify-save of targets.json without the lock, so a concurrent
// apply's just-written Files/Keys got clobbered.
func assertCommandBlocksOnLock(t *testing.T, env map[string]string, tmp string, args ...string) {
	t.Helper()
	lockPath := filepath.Join(tmp, ".agentsync", ".state", "agentsync.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	holder, err := iox.AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("seed-hold lock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, e := runCLI(t, env, args...)
		done <- e
	}()

	select {
	case <-done:
		_ = holder.Release()
		t.Fatalf("%v returned while the global lock was held; it does not serialize against apply", args)
	case <-time.After(2 * time.Second):
		// Still blocked on lock acquisition — correct.
	}

	_ = holder.Release()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("%v failed after lock release: %v", args, e)
		}
	case <-time.After(35 * time.Second):
		t.Fatalf("%v never completed after lock release", args)
	}
}

func TestMarketplaceAdd_AcquiresGlobalLock(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	mpDir := makeLocalMarketplace(t, t.TempDir())
	assertCommandBlocksOnLock(t, env, tmp, "marketplace", "add", mpDir)
}

func TestAgentDisablePurge_AcquiresGlobalLock(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	mcpFile := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	if err := os.MkdirAll(filepath.Dir(mcpFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpFile, []byte("[server]\ntype = \"stdio\"\ncommand = \"npx\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	assertCommandBlocksOnLock(t, env, tmp, "agent", "disable", "claude", "--purge")
}
