package cli

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// ensureStateGitignore is reachable from the first-run upgrade notice, which is
// deliberately LOCK-FREE — so unlike every other write in the tool it can run
// concurrently with itself. The file it touches is a user's own .gitignore in a
// repo they commit, which makes silent data loss here expensive.

// TestEnsureStateGitignore_ConcurrentWritersPreserveUserRules is the regression
// for that loss.
//
// The first fix used a plain read-modify-write, and a reader landing inside
// another writer's O_TRUNC window wrote back its short read. The SECOND fix
// reached for iox.AtomicWrite and did not help: it uses a fixed sibling temp
// name, so N concurrent writers share one temp inode and the same collapse
// reproduces (measured: 4000 lines to 1). Appending with O_APPEND is what
// actually holds.
func TestEnsureStateGitignore_ConcurrentWritersPreserveUserRules(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()

	var want strings.Builder
	for i := 0; i < 2000; i++ {
		want.WriteString("user-rule-" + strings.Repeat("x", 20) + "-" + itoa(i) + "\n")
	}
	path := filepath.Join(home, ".gitignore")
	if err := os.WriteFile(path, []byte(want.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ensureStateGitignore(home)
		}()
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	kept := strings.Count(string(got), "user-rule-")
	if kept != 2000 {
		t.Fatalf("concurrent ensureStateGitignore destroyed the user's rules: %d of 2000 survived "+
			"(a truncating write lost them)", kept)
	}
	if !strings.Contains(string(got), "/.state/") {
		t.Fatalf(".state/ is not ignored after the concurrent run:\n%s", tail(string(got)))
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
