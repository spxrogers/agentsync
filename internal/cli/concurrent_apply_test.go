package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/iox"
)

// TestConcurrentApply_LockSerializes is the spec-required test from
// docs/superpowers/specs/2026-05-04-agentsync-design.md line 483:
// "spawn two `apply` invocations against the same AGENTSYNC_TARGET_ROOT,
// assert serialization." We can't actually fork the process from a Go
// test cheaply, so we model the same condition by holding the global
// lock externally and asserting a fresh apply blocks (returns a clear
// "another agentsync process running?" error after the lock timeout,
// lowered from the 30s production default via AGENTSYNC_LOCK_TIMEOUT_MS
// so the suite doesn't wait out the full window).
//
// The contract under test: a second apply against the same lockfile
// MUST NOT proceed concurrently with the first. flock guarantees this
// at the OS level; the test asserts the CLI surfaces it as a clear
// error rather than blocking forever or silently racing.
func TestConcurrentApply_LockSerializes(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmp,
		// Wait out the lock in milliseconds, not the 30s production default —
		// the env override lock.go exposes for exactly this (see lock_test.go).
		"AGENTSYNC_LOCK_TIMEOUT_MS": "400",
	}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}

	// Hold the lock from another goroutine using the same iox helper
	// the CLI uses. The CLI's apply path must observe contention and
	// return an error rather than proceeding.
	lockPath := filepath.Join(tmp, ".agentsync", ".state", "agentsync.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	holder, err := iox.AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("could not seed-hold lock: %v", err)
	}
	defer func() { _ = holder.Release() }()

	// Race a real apply against the held lock. We accept either:
	//   1. error containing "busy" (the canonical contention message), or
	//   2. error containing "another agentsync process running"
	// Either is proof of serialization; both make the failure mode
	// obvious to a human user.
	done := make(chan struct {
		out string
		err error
	}, 1)
	// Every t.Setenv must happen HERE, on the test goroutine, before spawning:
	// if the timeout arm below t.Fatals while the goroutine is still inside
	// runCLI, a t.Setenv from that surviving goroutine would panic ("Setenv
	// used after test ended"). Pre-set the env and pass nil to the goroutine's
	// runCLI so it performs no Setenv of its own.
	setenvAll(t, env)
	go func() {
		// Subtle: the CLI tests share process state through cobra's
		// flag mutation; isolate this run inside a goroutine and wait
		// with a deadline so a hang would still fail the test rather
		// than blocking forever.
		out, runErr := runCLI(t, nil, "apply")
		done <- struct {
			out string
			err error
		}{out, runErr}
	}()

	// flock contention surfaces within the 400ms test lockTimeout. Cap the
	// test deadline well above that to allow CI variance.
	select {
	case res := <-done:
		if res.err == nil {
			t.Fatalf("apply with externally-held lock should fail, got success:\n%s", res.out)
		}
		msg := res.err.Error()
		lower := strings.ToLower(msg)
		if !strings.Contains(lower, "busy") && !strings.Contains(lower, "another agentsync") {
			t.Fatalf("apply error should name contention; got: %v", res.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("apply never returned; lock contention not surfaced as error")
	}
}

// TestConcurrentApply_DryRunDoesNotContend asserts the dry-run path is
// not gated on the global lock (sister regression to the apply-skip-
// lock-on-dry-run fix), so a long real apply does not block previews.
func TestConcurrentApply_DryRunDoesNotContend(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}

	lockPath := filepath.Join(tmp, ".agentsync", ".state", "agentsync.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	holder, err := iox.AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("could not seed-hold lock: %v", err)
	}
	defer func() { _ = holder.Release() }()

	// Dry-run must finish promptly even with the lock held. Setenv is hoisted
	// onto the test goroutine (see TestConcurrentApply_LockSerializes); the
	// goroutine's runCLI gets nil env so it never calls t.Setenv itself.
	setenvAll(t, env)
	done := make(chan error, 1)
	go func() {
		_, runErr := runCLI(t, nil, "apply", "--dry-run")
		done <- runErr
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dry-run should succeed under contention: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dry-run blocked behind held lock; should not acquire it")
	}
}
