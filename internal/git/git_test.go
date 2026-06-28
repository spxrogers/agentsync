package git

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// writeFile writes content to <dir>/<rel>, creating parents.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// readFile reads <dir>/<rel>.
func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// commitFile writes rel then commits it through the agentsync API, returning the
// new hash.
func commitFile(t *testing.T, r *Repo, dir, rel, content, msg string) string {
	t.Helper()
	writeFile(t, dir, rel, content)
	h, err := r.Commit([]string{rel}, msg, DefaultIdentity)
	if err != nil {
		t.Fatalf("commit %s: %v", rel, err)
	}
	if h == "" {
		t.Fatalf("commit %s returned empty hash", rel)
	}
	return h
}

func TestDetect(t *testing.T) {
	testenv.RequireContainer(t)

	t.Run("untracked", func(t *testing.T) {
		st, err := Detect(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if st != StateUntracked {
			t.Fatalf("got %v, want StateUntracked", st)
		}
	})

	t.Run("agentsync-owned", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := Init(dir); err != nil {
			t.Fatal(err)
		}
		st, err := Detect(dir)
		if err != nil {
			t.Fatal(err)
		}
		if st != StateAgentsyncOwned {
			t.Fatalf("got %v, want StateAgentsyncOwned", st)
		}
	})

	t.Run("foreign", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := gogit.PlainInit(dir, false); err != nil {
			t.Fatal(err)
		}
		st, err := Detect(dir)
		if err != nil {
			t.Fatal(err)
		}
		if st != StateForeign {
			t.Fatalf("got %v, want StateForeign", st)
		}
	})

	t.Run("nested in foreign repo", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := gogit.PlainInit(dir, false); err != nil {
			t.Fatal(err)
		}
		child := filepath.Join(dir, "sub", "deep")
		if err := os.MkdirAll(child, 0o700); err != nil {
			t.Fatal(err)
		}
		st, err := Detect(child)
		if err != nil {
			t.Fatal(err)
		}
		if st != StateForeign {
			t.Fatalf("got %v, want StateForeign (DetectDotGit should find the parent repo)", st)
		}
	})
}

func TestInit(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Dir() != dir {
		t.Fatalf("Dir()=%q want %q", r.Dir(), dir)
	}
	if fi, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !fi.IsDir() {
		t.Fatalf(".git not created: %v", err)
	}
	notice := filepath.Join(dir, NoticeFile)
	fi, err := os.Stat(notice)
	if err != nil {
		t.Fatalf("%s not created: %v", NoticeFile, err)
	}
	if runtime.GOOS != "windows" {
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", NoticeFile, got)
		}
		if gi, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			if got := gi.Mode().Perm(); got != 0o700 {
				t.Errorf(".git mode = %o, want 700", got)
			}
		}
	}
	st, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st != StateAgentsyncOwned {
		t.Fatalf("Detect after Init = %v, want StateAgentsyncOwned", st)
	}
}

func TestCommit(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	h1 := commitFile(t, r, dir, "settings.json", `{"a":1}`, "first")

	// Author identity is the dedicated agentsync identity.
	repo, _ := gogit.PlainOpen(dir)
	ref, _ := repo.Head()
	c, _ := repo.CommitObject(ref.Hash())
	if c.Author.Name != "agentsync" || c.Author.Email != "agentsync@localhost" {
		t.Errorf("author = %s <%s>, want agentsync <agentsync@localhost>", c.Author.Name, c.Author.Email)
	}

	// A second commit of a changed file gets a new hash whose parent is the first.
	h2 := commitFile(t, r, dir, "settings.json", `{"a":2}`, "second")
	if h2 == h1 {
		t.Fatalf("second commit reused the first hash")
	}
	repo, _ = gogit.PlainOpen(dir)
	ref, _ = repo.Head()
	c2, _ := repo.CommitObject(ref.Hash())
	if c2.NumParents() != 1 {
		t.Fatalf("second commit has %d parents, want 1", c2.NumParents())
	}
	if p, _ := c2.Parent(0); p.Hash.String() != h1 {
		t.Fatalf("second commit parent = %s, want %s", p.Hash.String(), h1)
	}

	// No change → no new checkpoint.
	h3, err := r.Commit([]string{"settings.json"}, "noop", DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if h3 != "" {
		t.Fatalf("commit with no change returned %q, want empty", h3)
	}
}

func TestStageTrackedDeletions(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "mcp.json", `{}`, "add mcp.json")

	// Remove the tracked file + drop an UNtracked user file alongside it.
	if err := os.Remove(filepath.Join(dir, "mcp.json")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "user-note.txt", "mine, do not touch")

	staged, err := r.StageTrackedDeletions()
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 1 || staged[0] != "mcp.json" {
		t.Fatalf("staged deletions = %v, want [mcp.json]", staged)
	}
	if _, err := r.CommitStaged("remove mcp.json", DefaultIdentity); err != nil {
		t.Fatal(err)
	}
	// The untracked user file is still present and was never committed.
	if _, err := os.Stat(filepath.Join(dir, "user-note.txt")); err != nil {
		t.Fatalf("untracked user file disturbed: %v", err)
	}
}

func TestLogAndResolve(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "a.txt", "1", "c1")
	commitFile(t, r, dir, "a.txt", "2", "c2")
	h3 := commitFile(t, r, dir, "a.txt", "3", "c3")

	all, err := r.Log(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("Log(0) len = %d, want 3", len(all))
	}
	if all[0].Subject != "c3" || all[2].Subject != "c1" {
		t.Fatalf("Log not newest-first: %q … %q", all[0].Subject, all[2].Subject)
	}
	if all[0].Hash != h3 {
		t.Fatalf("Log[0].Hash = %s, want %s", all[0].Hash, h3)
	}

	two, err := r.Log(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(two) != 2 {
		t.Fatalf("Log(2) len = %d, want 2", len(two))
	}

	prev, err := r.Resolve("HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if prev != all[1].Hash {
		t.Fatalf("Resolve(HEAD~1) = %s, want %s", prev, all[1].Hash)
	}
	if _, err := r.Resolve("deadbeefdeadbeef"); err == nil {
		t.Fatal("Resolve of a bogus rev should error")
	}
}

func TestRestoreAppendOnly(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "a.txt", "v1", "apply 1")    // HEAD~2 target
	commitFile(t, r, dir, "a.txt", "v2", "apply 2")    // HEAD~1
	commitFile(t, r, dir, "b.txt", "added", "apply 3") // HEAD (a.txt still v2)

	before, _ := r.Log(0)
	if len(before) != 3 {
		t.Fatalf("setup: want 3 commits, got %d", len(before))
	}

	// Dry-run plan: restoring HEAD~2 should restore a.txt and delete b.txt.
	target, changes, err := r.Plan("HEAD~2")
	if err != nil {
		t.Fatal(err)
	}
	if target != before[2].Hash {
		t.Fatalf("Plan target = %s, want %s", target, before[2].Hash)
	}
	kinds := map[string]string{}
	for _, c := range changes {
		kinds[c.Path] = c.Kind
	}
	if kinds["a.txt"] != "modify" || kinds["b.txt"] != "delete" {
		t.Fatalf("Plan changes = %v, want a.txt:modify b.txt:delete", kinds)
	}
	// Plan must not have written anything.
	if got := readFile(t, dir, "a.txt"); got != "v2" {
		t.Fatalf("Plan mutated worktree: a.txt = %q", got)
	}

	h, err := r.Restore("HEAD~2", "agentsync revert: test", DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if h == "" {
		t.Fatal("Restore returned empty hash")
	}
	// Worktree now matches the HEAD~2 checkpoint.
	if got := readFile(t, dir, "a.txt"); got != "v1" {
		t.Fatalf("after restore a.txt = %q, want v1", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("after restore b.txt should be gone, stat err = %v", err)
	}
	// Append-only: a new commit on top; all three originals still reachable.
	after, _ := r.Log(0)
	if len(after) != 4 {
		t.Fatalf("after restore Log len = %d, want 4 (append-only)", len(after))
	}
	repo, _ := gogit.PlainOpen(dir)
	ref, _ := repo.Head()
	head, _ := repo.CommitObject(ref.Hash())
	if p, _ := head.Parent(0); p.Hash.String() != before[0].Hash {
		t.Fatalf("revert parent = %s, want prior HEAD %s", p.Hash.String(), before[0].Hash)
	}

	// Restoring to the current HEAD is a no-op.
	noop, err := r.Restore("HEAD", "noop", DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if noop != "" {
		t.Fatalf("restore to HEAD returned %q, want empty (no-op)", noop)
	}
}
