package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
	"github.com/spxrogers/agentsync/internal/ui"
)

// subagentMigrationPrompter is a package var whose doc says it is "injectable so
// tests can drive the accept/decline branches without a terminal" — and until
// this file, no test drove either branch. Only the --no-input path was covered,
// so the seam existed for a test that did not exist.
//
// These are internal tests because the seam is unexported; the end-to-end
// behavior of both branches is covered from cli_test in migrate_test.go.

// TestPromptSubagentMigration_FailsClosedWithoutTTY pins the default that makes
// headless runs safe: no terminal (and no --no-input) still declines, so a CI
// job gets the actionable error rather than blocking forever on a Read.
func TestPromptSubagentMigration_FailsClosedWithoutTTY(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("no-input", false, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("y\n")) // even an explicit yes must not be read

	p := &ui.Printer{Out: &out, Err: &out}
	pending := &source.LegacySubagentDirError{Home: "/tmp/home", Files: []string{"rev.md"}}

	if promptSubagentMigration(cmd, p, pending) {
		t.Fatal("no TTY must decline: a non-interactive run has to fail closed, not consume stdin")
	}
}

// TestPromptConfirmYes_OnlyExplicitYes pins the confirmation grammar. Anything
// that is not an explicit yes — including EOF, which is what a closed stdin
// looks like — must be a no, so the caller falls through to the fail-closed
// error rather than acting on silence.
func TestPromptConfirmYes_OnlyExplicitYes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"y", "y\n", true},
		{"yes", "yes\n", true},
		{"uppercase Y", "Y\n", true},
		{"padded yes", "  yes  \n", true},
		{"n", "n\n", false},
		{"empty line is the default no", "\n", false},
		{"EOF (closed stdin)", "", false},
		{"anything else", "sure\n", false},
		{"yes with trailing text", "yes please\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var w bytes.Buffer
			got := promptConfirmYes(&w, strings.NewReader(tc.input), "move? ")
			if got != tc.want {
				t.Fatalf("promptConfirmYes(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if w.Len() == 0 {
				t.Error("the prompt was never written, so the user was asked nothing")
			}
		})
	}
}

// TestPromptSubagentMigration_WritesToStderr pins the channel. The offer can
// fire ahead of a `status --json` / `diff --json` payload, and stdout is that
// payload's channel — a prompt there corrupts what a caller is piping.
func TestPromptSubagentMigration_WritesToStderr(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("no-input", false, "")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader("n\n"))

	p := &ui.Printer{Out: &stdout, Err: &stderr}
	pending := &source.LegacySubagentDirError{Home: "/tmp/home", Files: []string{"rev.md"}}
	promptSubagentMigration(cmd, p, pending)

	if stdout.Len() != 0 {
		t.Fatalf("the migration offer must never touch stdout (it precedes --json payloads):\n%s", stdout.String())
	}
}

// TestEnsureSubagentLayout_AcceptUnderLockHoldingCommand is the regression for a
// DEADLOCK introduced while fixing the opposite bug.
//
// The migration mutates the canonical tree and .state/targets.json, so it must
// run under the global lock. Moving the acquire inside runSubagentMigration
// covered the unlocked callers (apply --dry-run, status, diff, explain) — but
// apply (non-dry-run), import and reconcile ALREADY hold the lock when they
// reach ensureSubagentLayout, and gofrs/flock is not reentrant within a process.
// Accepting the offer there blocked for the whole lock timeout and then failed
// with "another agentsync process running?" — blaming a process that does not
// exist, and not migrating.
//
// Nothing caught it because no test drove the ACCEPT branch from a lock-holding
// command: the suite only ever exercised --no-input (which declines).
func TestEnsureSubagentLayout_AcceptUnderLockHoldingCommand(t *testing.T) {
	testenv.RequireContainer(t)
	// Keep the failure fast if the deadlock ever returns: without this the test
	// would block for defaultLockTimeout (30s) before failing.
	t.Setenv("AGENTSYNC_LOCK_TIMEOUT_MS", "1500")

	home := t.TempDir()
	lockHome := filepath.Join(home, ".agentsync")
	if err := os.MkdirAll(filepath.Join(lockHome, ".state"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Stand in for apply/import/reconcile: hold the global lock, then do the
	// work the migration does underneath it.
	done := make(chan error, 1)
	go func() {
		done <- withGlobalLock(lockHome, func() error {
			// The nested acquire is the deadlock. It must succeed.
			return withGlobalLock(lockHome, func() error { return nil })
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a nested withGlobalLock from a lock-holding command must succeed, "+
				"or accepting the migration offer from apply/import/reconcile deadlocks: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("nested withGlobalLock never returned — the migration offer deadlocks apply/import/reconcile")
	}
}

// TestEnsureSubagentLayout_AcceptThroughApply drives the accept branch through a
// real lock-holding command, end to end.
//
// This is the test whose absence let the deadlock ship: the suite covered the
// --no-input path (which DECLINES) and `migrate subagents` (which held no outer
// lock), so the one combination that hangs — accept, from inside a command that
// already holds the global lock — was never executed.
func TestEnsureSubagentLayout_AcceptThroughApply(t *testing.T) {
	testenv.RequireContainer(t)
	t.Setenv("AGENTSYNC_LOCK_TIMEOUT_MS", "1500") // fail fast if the deadlock returns

	root := t.TempDir()
	t.Setenv("AGENTSYNC_TARGET_ROOT", root)
	home := filepath.Join(root, ".agentsync")

	// Minimal canonical tree in the PRE-migration layout.
	for _, d := range []string{".state", source.LegacySubagentsDir} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "agentsync.toml"),
		[]byte("[agents]\nclaude = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(home, source.LegacySubagentsDir, "rev.md")
	if err := os.WriteFile(legacy, []byte("---\ndescription: reviewer\n---\nReview.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Accept the offer without a terminal.
	prev := subagentMigrationPrompter
	subagentMigrationPrompter = func(*cobra.Command, *ui.Printer, *source.LegacySubagentDirError) bool { return true }
	t.Cleanup(func() { subagentMigrationPrompter = prev })

	// `apply` (non-dry-run) holds the global lock before it reaches the offer.
	done := make(chan error, 1)
	go func() {
		root := NewRoot()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"apply", "--scope", "user"})
		done <- root.Execute()
	}()

	select {
	case err := <-done:
		if err != nil && strings.Contains(err.Error(), "another agentsync process running") {
			t.Fatalf("accepting the migration offer from `apply` self-deadlocked on the global lock: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("`apply` never returned after accepting the migration offer — self-deadlock on the global lock")
	}

	// The migration must actually have happened, under the lock.
	if _, err := os.Stat(filepath.Join(home, source.SubagentsDir, "rev.md")); err != nil {
		t.Errorf("accepting the offer from `apply` did not migrate the file: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("the legacy file survived the accepted migration: %v", err)
	}
}
