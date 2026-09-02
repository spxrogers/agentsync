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

	"github.com/spf13/cobra"

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
			// Swap the applied destination for a FIFO, and RESTORE it after.
			// Removing the FIFO is not enough: the subtests share one applied
			// home, so leaving the path absent means the next shape's subtest
			// runs against a home that was never fully applied — contradicting
			// this test's own premise.
			//
			// That is a structural property, not a measured pass/hang flip. An
			// earlier version of this comment claimed the un-skipped rows
			// changed outcome without the restore; eight configurations could
			// not reproduce that. What actually varies there is goroutines left
			// parked in open(2) by PRECEDING timed-out rows, which can pair
			// with a later opener — nondeterministic, and orthogonal to this.
			applied, err := os.ReadFile(dest.path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(dest.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(dest.path); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(dest.path, 0o600); err != nil {
				// Safe to write directly here: mkfifo failed, so the path is
				// still absent rather than a FIFO.
				_ = os.WriteFile(dest.path, applied, info.Mode().Perm())
				t.Skipf("mkfifo unsupported here: %v", err)
			}
			t.Cleanup(func() { restoreDest(t, dest.path, applied, info.Mode().Perm()) })

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
// within d, or if the command never actually ran. The environment must already
// be set by the caller on the test goroutine (runCLI's t.Setenv persists for the
// whole test), so nothing here touches testing.T off the test goroutine except
// the final t.Fatalf.
func runBounded(t *testing.T, d time.Duration, args ...string) {
	t.Helper()
	ran, err := runBoundedE(t, d, args...)
	if !ran {
		t.Fatalf("`agentsync %s` never reached its command body (err=%v) — the row executed "+
			"none of the code it names, so \"it returned\" proves nothing", strings.Join(args, " "), err)
	}
}

// runBoundedE runs the CLI in a goroutine, bounded by d, and reports whether the
// resolved command's body actually STARTED. It REPORTS that verdict rather than
// failing on it, so the anti-vacuity check itself can be tested; runBounded is
// the fatal wrapper every real row uses. (The timeout path still fails here —
// a wedged command has no verdict to report.)
//
// `ran` is OBSERVED, by wrapping the resolved command's RunE, rather than
// inferred from cobra's error text. Two earlier versions inferred it and both
// were wrong: the first matched substrings against the output buffer, which
// NewRoot's SilenceErrors leaves empty, so it could never fire — while the
// buffer's ordinary stdout made it fire on user content instead. The second
// matched cobra's error prose, which covers only the arg/flag layer: anything
// rejecting later still scored as "it ran", including this repo's own
// enforceScopeStance (internal/cli/scope_flags.go), a PersistentPreRunE refusal
// that never reaches RunE. Wrapping the body answers the actual question and
// cannot drift with cobra's wording.
func runBoundedE(t *testing.T, d time.Duration, args ...string) (ran bool, err error) {
	t.Helper()
	detachSlog(t)
	type result struct {
		out string
		err error
		ran bool
	}
	done := make(chan result, 1)
	go func() {
		var buf bytes.Buffer
		root := cli.NewRoot()
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs(args)

		started := false
		if cmd, _, ferr := root.Find(args); ferr == nil && cmd != nil {
			switch {
			case cmd.RunE != nil:
				inner := cmd.RunE
				cmd.RunE = func(c *cobra.Command, a []string) error {
					started = true
					return inner(c, a)
				}
			case cmd.Run != nil:
				inner := cmd.Run
				cmd.Run = func(c *cobra.Command, a []string) {
					started = true
					inner(c, a)
				}
			}
		}
		rerr := root.Execute()
		done <- result{buf.String(), rerr, started}
	}()
	select {
	case r := <-done:
		return r.ran, r.err
	case <-time.After(d):
		t.Fatalf("`agentsync %s` BLOCKED on a non-regular destination — os.ReadFile on a "+
			"FIFO waits for a writer that never comes, so the read's own error path never "+
			"runs and the command never returns", strings.Join(args, " "))
		return false, nil
	}
}

// TestRunBoundedDetectsACommandThatNeverRan is the positive control for
// runBounded's anti-vacuity check. Without it the check is unfalsifiable: it
// only ever fires on a broken invocation, so nothing in a green suite proves it
// still works — and its first two versions shipped unable to fire at all.
//
// It drives runBoundedE, which reports instead of failing, so a row that never
// ran can be asserted rather than crashing the test.
func TestRunBoundedDetectsACommandThatNeverRan(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		// Exactly the invocation that silently passed for two rounds: `import`
		// without the agent selector its ExactArgs(1) requires.
		{name: "a missing required argument", args: []string{"import"}},
		{name: "an unknown command", args: []string{"nosuchcommand"}},
		// The case cobra's error prose could not catch: a refusal from
		// PersistentPreRunE, which runs AFTER argument validation and never
		// reaches the command body. `secret list` is scope-unaware, so passing
		// --scope is refused by enforceScopeStance.
		{name: "a refusal from PersistentPreRunE", args: []string{"secret", "list", "--scope", "user"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran, err := runBoundedE(t, 8*time.Second, tc.args...)
			if err == nil {
				t.Fatalf("`agentsync %s` returned nil; expected it to be refused",
					strings.Join(tc.args, " "))
			}
			if ran {
				t.Errorf("runBoundedE reported ran=true for %q, which was refused with %v — "+
					"runBounded would let this row pass as if the command had executed",
					strings.Join(tc.args, " "), err)
			}
		})
	}

	// The other direction: a command that DOES run must report ran=true, or
	// every real row would fail as vacuous.
	t.Run("a command that runs is reported as having run", func(t *testing.T) {
		if ran, _ := runBoundedE(t, 8*time.Second, "version"); !ran {
			t.Error("runBoundedE reported ran=false for `version`, which has no arguments to " +
				"get wrong — the wrap is not finding the resolved command")
		}
	})
}

// restoreDest puts a captured destination back, by RENAME rather than by
// writing to the path.
//
// os.WriteFile opens the destination, and opening a FIFO blocks until the other
// end appears — inside t.Cleanup, where no per-row timeout applies, that wedges
// the suite outright. Worse, when a reader IS present — which is what a
// timed-out row leaves parked in open(2), and becomes common once the #241/#242
// skips are deleted — the write succeeds, drains into the pipe, returns nil,
// and leaves the FIFO in place, so an error check never fires and the
// destination is silently not restored. Rename never opens the target.
// TestRestoreDestReplacesAFIFOEvenWithAReaderAttached measures exactly that.
func restoreDest(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	tmp := path + ".restore"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		t.Errorf("restoring %s: %v", path, err)
		return
	}
	// os.WriteFile's mode is masked by umask on create; chmod pins it.
	if err := os.Chmod(tmp, mode); err != nil {
		t.Errorf("restoring %s: %v", path, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Errorf("restoring %s: %v — the next subtest would run against a "+
			"partially-applied home", path, err)
	}
}

// TestRestoreDestReplacesAFIFOEvenWithAReaderAttached pins the reason
// restoreDest renames instead of writing.
//
// The reader goroutine reproduces what a timed-out row leaves behind. Without
// the rename, os.WriteFile succeeds here — the bytes vanish into the pipe, the
// error is nil, and the FIFO survives, so the destination is silently NOT
// restored and the next subtest runs against a partially-applied home.
func TestRestoreDestReplacesAFIFOEvenWithAReaderAttached(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dest")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}

	// A reader ATTACHED to the FIFO, standing in for what a timed-out row leaves
	// parked in open(2).
	//
	// O_NONBLOCK is load-bearing. A blocking os.Open in a goroutine looks like
	// the real thing but is a RACE against restoreDest: if the rename lands
	// first the open returns instantly on a regular file, and if the reader
	// wins it parks forever, because restoreDest's entire purpose is never to
	// open the target. The first version of this test did exactly that and hung
	// most of the time — reproducing, in the test meant to prevent it, the
	// failure mode this whole PR is about. O_NONBLOCK returns immediately and
	// leaves a genuinely attached reader, so the counterfactual below (a direct
	// os.WriteFile succeeding into the pipe) is real and deterministic.
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("attaching a reader to %s: %v", path, err)
	}
	defer func() { _ = syscall.Close(fd) }()

	// Called on the test goroutine: with a reader attached neither restoreDest
	// nor the os.WriteFile variant can block, so no timeout wrapper is needed —
	// and restoreDest's t.Errorf stays on the goroutine that owns the test.
	restoreDest(t, path, []byte("applied"), 0o644)

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("destination is %s after restore, want a regular file: the write went INTO "+
			"the FIFO instead of replacing it, so the fixture was never restored",
			info.Mode().Type())
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "applied" {
		t.Errorf("restored content = (%q, %v), want %q", got, err, "applied")
	}
}
