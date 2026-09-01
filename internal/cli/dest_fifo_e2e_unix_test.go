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

			// Only the three commands this change actually fixes. `apply`,
			// `apply --dry-run` and `import <agent>` still hang on this exact
			// fixture — their reads are in internal/render and the adapter
			// Ingest paths, which this gate does not cover (issues #241, #242).
			// Asserting them here would be asserting a bug.
			for _, args := range [][]string{
				{"status"},
				{"diff"},
				{"reconcile", "--auto-safe"},
			} {
				t.Run(strings.Join(args, " "), func(t *testing.T) {
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
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		root := cli.NewRoot()
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs(args)
		_ = root.Execute()
		done <- buf.String()
	}()
	select {
	case out := <-done:
		// Anti-vacuity. An earlier version of this test ran `import` with no
		// agent selector; cobra rejected it at ExactArgs(1) before RunE, so the
		// row executed zero lines of the code it named and passed identically
		// with the fix reverted. A command that never starts cannot hang, so
		// "it returned" is only meaningful once we know it ran.
		if strings.Contains(out, "arg(s), received") || strings.Contains(out, "unknown command") {
			t.Fatalf("`agentsync %s` never reached its RunE — cobra rejected the invocation: %s",
				strings.Join(args, " "), strings.TrimSpace(out))
		}
		return out
	case <-time.After(d):
		t.Fatalf("`agentsync %s` BLOCKED on a non-regular destination — os.ReadFile on a "+
			"FIFO waits for a writer that never comes, so the read's own error path never "+
			"runs and the command never returns", strings.Join(args, " "))
		return ""
	}
}
