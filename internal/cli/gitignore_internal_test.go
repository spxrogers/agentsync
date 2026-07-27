package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// ensureStateGitignore is reachable from the first-run upgrade notice, which is
// deliberately LOCK-FREE — so unlike every other write in the tool it can run
// concurrently with itself. The file it touches is a user's own .gitignore in a
// repo they commit, which makes silent data loss here expensive.

// gitignoreHelperEnv marks a re-exec of this test binary as the concurrency
// helper: it runs ensureStateGitignore once against the named home and exits.
const (
	gitignoreHelperEnv     = "AGENTSYNC_TEST_GITIGNORE_HELPER"
	gitignoreHelperHome    = "AGENTSYNC_TEST_GITIGNORE_HOME"
	gitignoreHelperBarrier = "AGENTSYNC_TEST_GITIGNORE_BARRIER"
)

// TestEnsureStateGitignoreHelperProcess is not a test. It is the child process
// spawned by the concurrency test below (the standard Go helper-process
// pattern); it is inert unless the marker env var is set.
func TestEnsureStateGitignoreHelperProcess(t *testing.T) {
	if os.Getenv(gitignoreHelperEnv) != "1" {
		t.Skip("helper process; runs only when re-executed by the concurrency test")
	}
	// Spin on the barrier so every child enters the critical section at the same
	// moment. Without it the first writer wins the race, adds the rule, and
	// every other process short-circuits on "already ignored" — leaving exactly
	// one unsynchronized write and no race at all.
	if barrier := os.Getenv(gitignoreHelperBarrier); barrier != "" {
		for i := 0; i < 20000; i++ {
			if _, err := os.Stat(barrier); err == nil {
				break
			}
			time.Sleep(200 * time.Microsecond)
		}
	}
	_ = ensureStateGitignore(os.Getenv(gitignoreHelperHome))
	os.Exit(0)
}

// TestEnsureStateGitignore_ConcurrentWritersPreserveUserRules is the regression
// for silent data loss in a user's own .gitignore.
//
// The first fix used a plain read-modify-write, and a reader landing inside
// another writer's O_TRUNC window wrote back its short read. The SECOND fix
// reached for iox.AtomicWrite and did not help: it uses a fixed sibling temp
// name, so N concurrent writers share one temp inode and the same collapse
// reproduces (measured: 4000 lines to 1). Appending with O_APPEND is what
// actually holds.
//
// THIS IS A SMOKE TEST, NOT THE REGRESSION GUARD — read
// TestEnsureStateGitignoreDoesNotUseATruncatingWrite below for that, and this
// comment for why.
//
// The first version used goroutines and passed 10/10 against the KNOWN-BROKEN
// implementation: under -race, which is what CI runs, the detector's
// serialization closes the window entirely. Re-executing this binary (the real
// shape: many short-lived invocations racing on one home) does catch it — but
// only about 1 run in 5 even with a start barrier, because after the first
// writer adds the rule every other process short-circuits on "already
// ignored", so only the very first write ever races.
//
// A 1-in-5 detector is not something to gate a release on. What IS reliable is
// the structural property: O_APPEND cannot truncate, so the bug is impossible
// by construction rather than improbable. That is what the guard below asserts.
// This test stays because exercising the concurrent path end-to-end is still
// worth something, and it is cheap at these numbers.
func TestEnsureStateGitignore_ConcurrentWritersPreserveUserRules(t *testing.T) {
	testenv.RequireContainer(t)

	const (
		rules    = 2000
		writers  = 12
		attempts = 2
	)

	for attempt := 1; attempt <= attempts; attempt++ {
		home := t.TempDir()
		var seed strings.Builder
		for i := 0; i < rules; i++ {
			seed.WriteString("user-rule-" + strings.Repeat("x", 20) + "-" + itoa(i) + "\n")
		}
		path := filepath.Join(home, ".gitignore")
		if err := os.WriteFile(path, []byte(seed.String()), 0o644); err != nil {
			t.Fatal(err)
		}

		barrier := filepath.Join(t.TempDir(), "go")
		var wg sync.WaitGroup
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cmd := exec.Command(os.Args[0], "-test.run=TestEnsureStateGitignoreHelperProcess")
				cmd.Env = append(os.Environ(),
					gitignoreHelperEnv+"=1",
					gitignoreHelperHome+"="+home,
					gitignoreHelperBarrier+"="+barrier,
					"AGENTSYNC_TEST_IN_CONTAINER=1",
				)
				_ = cmd.Run()
			}()
		}
		// Give every child time to reach the barrier, then release them at once.
		time.Sleep(400 * time.Millisecond)
		if err := os.WriteFile(barrier, []byte("go"), 0o644); err != nil {
			t.Fatal(err)
		}
		wg.Wait()

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if kept := strings.Count(string(got), "user-rule-"); kept != rules {
			t.Fatalf("attempt %d: concurrent ensureStateGitignore destroyed the user's rules: "+
				"%d of %d survived — a truncating write lost them", attempt, kept, rules)
		}
		if !strings.Contains(string(got), "/.state/") {
			t.Fatalf("attempt %d: .state/ is not ignored after the concurrent run:\n%s", attempt, tail(string(got)))
		}
	}
}

// TestEnsureStateGitignore_WritesThroughSymlink pins the chezmoi / GNU Stow
// case: those tools symlink dotfiles into place, so replacing the link with a
// regular file detaches the user's managed copy. An earlier fix used
// iox.AtomicWrite, which REFUSES a symlinked destination — leaving .state/
// unignored for exactly those users, silently, on the best-effort notice path.
func TestEnsureStateGitignore_WritesThroughSymlink(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	real := filepath.Join(t.TempDir(), "managed-gitignore")
	if err := os.WriteFile(real, []byte("managed-rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".gitignore")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if err := ensureStateGitignore(home); err != nil {
		t.Fatalf("a symlinked .gitignore must still be updated: %v", err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was REPLACED by a regular file, detaching the user's managed copy")
	}
	body, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "/.state/") {
		t.Fatalf("the rule did not reach the symlink target:\n%s", body)
	}
	if !strings.Contains(string(body), "managed-rule") {
		t.Fatalf("the managed file's own content was lost:\n%s", body)
	}
}

// TestEnsureStateGitignore_PreservesModeAndIsIdempotent covers the two quieter
// regressions: a rewrite-based implementation chmods the file (600 -> 644), and
// a rule already present in an equivalent spelling must not gain a duplicate.
func TestEnsureStateGitignore_PreservesModeAndIsIdempotent(t *testing.T) {
	testenv.RequireContainer(t)

	t.Run("preserves an existing restrictive mode", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, ".gitignore")
		if err := os.WriteFile(path, []byte("secret-ish\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ensureStateGitignore(home); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode changed from 0600 to %04o", fi.Mode().Perm())
		}
	})

	for _, spelling := range []string{"/.state/", ".state/", "/.state", ".state"} {
		t.Run("no duplicate rule for "+spelling, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".gitignore")
			if err := os.WriteFile(path, []byte("a\n"+spelling+"\nb\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := ensureStateGitignore(home); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(string(got), ".state"); n != 1 {
				t.Errorf("%q already ignores .state/, but the file now has %d such rules:\n%s",
					spelling, n, got)
			}
		})
	}

	t.Run("creates the file when absent", func(t *testing.T) {
		home := t.TempDir()
		if err := ensureStateGitignore(home); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(home, ".gitignore"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), "/.state/") {
			t.Fatalf("created .gitignore does not ignore .state/:\n%s", got)
		}
	})

	t.Run("appends cleanly to a file with no trailing newline", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, ".gitignore")
		if err := os.WriteFile(path, []byte("no-trailing-newline"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ensureStateGitignore(home); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(got), "no-trailing-newline/.state/") {
			t.Fatalf("the rule was glued onto the last line:\n%s", got)
		}
		if !strings.Contains(string(got), "no-trailing-newline") {
			t.Fatalf("existing content lost:\n%s", got)
		}
	})
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func tail(s string) string {
	if len(s) > 400 {
		return s[len(s)-400:]
	}
	return s
}

// TestEnsureStateGitignoreDoesNotUseATruncatingWrite is the real regression
// guard for the .gitignore data loss, and it is deterministic where a
// concurrency test is not.
//
// Two implementations have already lost a user's rules here. Both failed the
// same way: they replaced the file's contents from a value read earlier, so a
// reader inside another writer's window wrote back a short read.
// os.WriteFile(O_TRUNC) does it directly; iox.AtomicWrite does it too, because
// its temp file has a FIXED name that concurrent writers share. Appending is
// the property that makes the failure impossible rather than unlikely — a
// POSIX O_APPEND write cannot truncate and cannot interleave with another
// append of this size.
//
// So assert the property, not the symptom: this function must open with
// O_APPEND and must not route through a whole-file writer.
func TestEnsureStateGitignoreDoesNotUseATruncatingWrite(t *testing.T) {
	src := readFileForGuard(t, repoRootFromCaller(t), "internal/cli/init.go")
	body := funcBody(src, "func ensureStateGitignore(")
	if body == "" {
		t.Fatal("ensureStateGitignore not found in init.go — update this guard")
	}

	if !strings.Contains(body, "os.O_APPEND") {
		t.Error("ensureStateGitignore no longer opens with os.O_APPEND. It is reachable from the " +
			"LOCK-FREE upgrade-notice path, so a whole-file write can truncate a user's .gitignore " +
			"under concurrent runs (measured: 4000 rules to 1). Append instead.")
	}
	for _, banned := range []string{"os.WriteFile(", "iox.AtomicWrite("} {
		if strings.Contains(body, banned) {
			t.Errorf("ensureStateGitignore uses %s, which REPLACES the file from a previously-read "+
				"value — the exact shape that lost a user's rules twice. iox.AtomicWrite is not a fix "+
				"here either: its temp file has a fixed name, so concurrent writers share one inode.",
				strings.TrimSuffix(banned, "("))
		}
	}
}
