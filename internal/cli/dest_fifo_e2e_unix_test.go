//go:build unix

package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/cli"
)

// TestCommandsDoNotHangOnNonRegularDestination is the end-to-end half of the
// destination-read guard: it drives the real commands against a real applied
// home, which is the only way to prove the guard actually sits on the path the
// user reaches.
//
// Both shapes are covered because they are read by different code. A whole-file
// destination goes through the per-op reads in `diff` and `reconcile`; a
// key-merge destination (~/.claude.json) goes through the shared readDestFile
// that every drift walk uses, including `status` — which is advertised as
// read-only and hung on it.
//
// Every run is bounded. runCLI executes the command IN-PROCESS, so an
// unguarded read does not fail the test, it wedges the whole test binary until
// the package timeout kills it with a stack dump and no useful diagnostic.
func TestCommandsDoNotHangOnNonRegularDestination(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	// A key-merge destination (~/.claude.json) …
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	if err := os.MkdirAll(filepath.Dir(mcp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// … and a whole-file one (a rendered skill).
	skill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	for _, dest := range []struct{ name, path string }{
		{"whole-file destination", filepath.Join(tmp, ".claude", "skills", "demo", "SKILL.md")},
		{"key-merge destination", filepath.Join(tmp, ".claude.json")},
	} {
		t.Run(dest.name, func(t *testing.T) {
			if _, err := os.Stat(dest.path); err != nil {
				t.Fatalf("fixture never applied %s: %v — this test would pass vacuously", dest.path, err)
			}
			// Swap the applied destination for a FIFO. Nothing here opens it.
			if err := os.Remove(dest.path); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(dest.path, 0o600); err != nil {
				t.Skipf("mkfifo unsupported here: %v", err)
			}
			t.Cleanup(func() { _ = os.Remove(dest.path) })

			// The first group is what this change fixes. The second still hangs
			// on this exact fixture and is SKIPPED rather than asserted or
			// omitted: asserting the hang costs 8s a row and would start
			// failing the day it is fixed, reading as a regression; omitting it
			// leaves nothing to find. A skip is greppable, shows up in -v, and
			// the person who closes the issue deletes one line to inherit a
			// ready-made assertion.
			for _, tc := range []struct {
				args []string
				skip string
			}{
				{args: []string{"status"}},
				{args: []string{"diff"}},
				{args: []string{"reconcile", "--auto-safe"}},
				{args: []string{"apply", "--dry-run"}, skip: "#241: render.Writer.Write's convergence read is unguarded"},
				{args: []string{"apply"}, skip: "#241: render.Writer.Write's convergence read is unguarded"},
				{args: []string{"reconcile", "--auto-override"}, skip: "#241: [o]verride queues into render.Writer.Write"},
				{args: []string{"import", "claude"}, skip: "#242: the adapter Ingest reads are unguarded"},
			} {
				args := tc.args
				t.Run(strings.Join(args, " "), func(t *testing.T) {
					if tc.skip != "" {
						t.Skip(tc.skip)
					}
					// Exit status is deliberately not asserted: a non-regular
					// destination may legitimately report drift, or refuse. The
					// contract under test is that the command RETURNS.
					runBounded(t, 8*time.Second, args...)
				})
			}
		})
	}
}

// runBounded executes the CLI in a goroutine and fails if it has not returned
// within d. The environment must already be set by the caller on the test
// goroutine (runCLI's t.Setenv persists for the whole test), so nothing here
// touches testing.T off the test goroutine except the final t.Fatalf.
func runBounded(t *testing.T, d time.Duration, args ...string) string {
	t.Helper()
	detachSlog(t)
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		var buf bytes.Buffer
		root := cli.NewRoot()
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs(args)
		err := root.Execute()
		done <- result{buf.String(), err}
	}()
	select {
	case r := <-done:
		// Anti-vacuity. An earlier version of this test ran `import` with no
		// agent selector; cobra rejected it at ExactArgs(1) before RunE, so the
		// row executed zero lines of the code it named and passed identically
		// with the fix reverted. A command that never starts cannot hang, so
		// "it returned" is only meaningful once we know it ran.
		//
		// The check MUST read the returned error, not the output buffer.
		// NewRoot sets SilenceErrors, so cobra prints nothing on a rejection —
		// the first version of this guard inspected the buffer, could therefore
		// never fire, and was itself the bug it was written to prevent. Worse,
		// the buffer carries ordinary stdout, so matching these substrings
		// against it made a plain `diff` fail whenever a rendered file happened
		// to contain the words. TestRunBoundedRejectsAnInvocationCobraRefuses is
		// the positive control that keeps this honest.
		if r.err != nil && isCobraRejection(r.err) {
			t.Fatalf("`agentsync %s` never reached its RunE — cobra rejected the invocation: %v",
				strings.Join(args, " "), r.err)
		}
		return r.out
	case <-time.After(d):
		t.Fatalf("`agentsync %s` BLOCKED on a non-regular destination — os.ReadFile on a "+
			"FIFO waits for a writer that never comes, so the read's own error path never "+
			"runs and the command never returns", strings.Join(args, " "))
		return ""
	}
}

// isCobraRejection reports whether err is cobra refusing the invocation itself
// — a wrong argument count, an unknown command or flag — as opposed to a real
// failure from inside the command.
//
// It matches the returned error only. NewRoot sets SilenceErrors, so none of
// this text is ever printed, which is what made the first version of this check
// (written against the output buffer) unable to fire.
func isCobraRejection(err error) bool {
	msg := err.Error()
	for _, marker := range []string{
		"arg(s), received",
		"unknown command",
		"unknown flag",
		"unknown shorthand flag",
		"requires at least",
		"accepts at most",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// TestRunBoundedRejectsAnInvocationCobraRefuses is the positive control for the
// anti-vacuity check in runBounded. Without it that check is unfalsifiable: it
// only ever fires on a broken invocation, so nothing in a green suite proves it
// still works, and its first version silently never fired at all.
//
// It asserts on isCobraRejection rather than by calling runBounded, because
// runBounded signals failure with t.Fatalf — driving it with a bad invocation
// would fail this test rather than pass it.
func TestRunBoundedRejectsAnInvocationCobraRefuses(t *testing.T) {
	// Exactly the invocations that silently passed before: `import` without the
	// agent selector its ExactArgs(1) requires, and an outright bad command.
	for _, args := range [][]string{{"import"}, {"nosuchcommand"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var buf bytes.Buffer
			root := cli.NewRoot()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(args)
			err := root.Execute()
			if err == nil {
				t.Fatalf("`agentsync %s` returned nil; expected cobra to refuse it",
					strings.Join(args, " "))
			}
			if !isCobraRejection(err) {
				t.Errorf("isCobraRejection(%q) = false, want true — runBounded would let this "+
					"invocation pass as if the command had run", err)
			}
			// The reason the check reads the error and not the buffer.
			if strings.Contains(buf.String(), "arg(s), received") ||
				strings.Contains(buf.String(), "unknown command") {
				t.Errorf("cobra printed its rejection into the output buffer (%q) — if that ever "+
					"becomes true, the simpler buffer-based check would work and this "+
					"indirection can go", buf.String())
			}
		})
	}
}
