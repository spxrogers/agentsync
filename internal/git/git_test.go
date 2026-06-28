package git

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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

// commitFile writes rel, stages it, and commits, returning the new hash.
func commitFile(t *testing.T, r *Repo, dir, rel, content, msg string) string {
	t.Helper()
	writeFile(t, dir, rel, content)
	if err := r.Stage([]string{rel}); err != nil {
		t.Fatalf("stage %s: %v", rel, err)
	}
	h, err := r.CommitStaged(msg, DefaultIdentity)
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

func TestOwnsExactly(t *testing.T) {
	testenv.RequireContainer(t)

	t.Run("no repo", func(t *testing.T) {
		got, err := OwnsExactly(t.TempDir())
		if err != nil || got {
			t.Fatalf("OwnsExactly(empty) = (%v, %v), want (false, nil)", got, err)
		}
	})
	t.Run("agentsync repo at dir", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := Init(dir); err != nil {
			t.Fatal(err)
		}
		if got, _ := OwnsExactly(dir); !got {
			t.Fatal("OwnsExactly should be true at an agentsync repo root")
		}
	})
	t.Run("foreign repo at dir", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := gogit.PlainInit(dir, false); err != nil {
			t.Fatal(err)
		}
		if got, _ := OwnsExactly(dir); got {
			t.Fatal("OwnsExactly must be false for a foreign (unmarked) repo")
		}
	})
	t.Run("child of an agentsync repo is NOT exact-owned", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := Init(dir); err != nil {
			t.Fatal(err)
		}
		child := filepath.Join(dir, "skills")
		if err := os.MkdirAll(child, 0o700); err != nil {
			t.Fatal(err)
		}
		// Detect (upward) would call this Owned; OwnsExactly (exact) must not.
		if got, _ := OwnsExactly(child); got {
			t.Fatal("OwnsExactly must be false for a dir merely nested inside an agentsync repo")
		}
		if st, _ := Detect(child); st != StateAgentsyncOwned {
			t.Fatal("precondition: Detect(child) should be Owned via DetectDotGit")
		}
	})
}

func TestHasNestedRepoBelow(t *testing.T) {
	testenv.RequireContainer(t)

	t.Run("none", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "a/b/c.txt", "x")
		if got, err := HasNestedRepoBelow(dir); err != nil || got {
			t.Fatalf("HasNestedRepoBelow(no nested) = (%v,%v), want false", got, err)
		}
	})
	t.Run("missing dir", func(t *testing.T) {
		if got, err := HasNestedRepoBelow(filepath.Join(t.TempDir(), "nope")); err != nil || got {
			t.Fatalf("HasNestedRepoBelow(missing) = (%v,%v), want (false,nil)", got, err)
		}
	})
	t.Run("nested .git dir at depth", func(t *testing.T) {
		dir := t.TempDir()
		deep := filepath.Join(dir, "x", "y")
		if _, err := gogit.PlainInit(deep, false); err != nil {
			t.Fatal(err)
		}
		if got, _ := HasNestedRepoBelow(dir); !got {
			t.Fatal("should find a nested .git directory at depth")
		}
	})
	t.Run("nested .git FILE (gitlink) is detected", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "sub/.git", "gitdir: /somewhere/else/.git\n")
		if got, _ := HasNestedRepoBelow(dir); !got {
			t.Fatal("should detect a nested .git gitlink FILE, not just a directory")
		}
	})
	t.Run("does not match the dir's own .git", func(t *testing.T) {
		// Init'd repo: its OWN .git must not count as "below". (In practice the guard
		// only runs on untracked dirs, but pin the invariant anyway.)
		dir := t.TempDir()
		if _, err := Init(dir); err != nil {
			t.Fatal(err)
		}
		// Remove the notice so the only entries are .git + nothing nested.
		_ = os.Remove(filepath.Join(dir, NoticeFile))
		if got, _ := HasNestedRepoBelow(dir); got {
			t.Fatal("the dir's own .git must not count as a nested repo below it")
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

// TestInitRefusesExistingRepo pins Init's documented invariant: it errors rather
// than re-initializing when a repo already exists at dir — whether that repo is the
// user's own (foreign) or one agentsync created earlier. Callers gate on Detect ==
// StateUntracked first, but Init must fail closed if that gate is ever bypassed.
func TestInitRefusesExistingRepo(t *testing.T) {
	testenv.RequireContainer(t)

	t.Run("over a foreign repo", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := gogit.PlainInit(dir, false); err != nil {
			t.Fatal(err)
		}
		if _, err := Init(dir); err == nil {
			t.Fatal("Init over a pre-existing foreign repo should error, got nil")
		}
		// The foreign repo must be left untouched — no marker stamped onto it.
		if owned, _ := OwnsExactly(dir); owned {
			t.Fatal("a failed Init must not stamp the agentsync marker onto a foreign repo")
		}
	})

	t.Run("over an agentsync-owned repo", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := Init(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := Init(dir); err == nil {
			t.Fatal("Init over an existing agentsync repo should error, got nil")
		}
	})
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
	if err := r.Stage([]string{"settings.json"}); err != nil {
		t.Fatal(err)
	}
	h3, err := r.CommitStaged("noop", DefaultIdentity)
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

// inHead reports whether rel is present in the repo's HEAD tree.
func inHead(t *testing.T, dir, rel string) bool {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := c.Tree()
	if err != nil {
		t.Fatal(err)
	}
	_, err = tree.File(rel)
	return err == nil
}

func TestSnapshotDirtyTrackedAndIsClean(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "a.txt", "v1", "first")

	if clean, _ := r.IsClean(); !clean {
		t.Fatal("freshly committed worktree should be clean")
	}

	// Hand-edit a tracked file + drop an untracked user file.
	writeFile(t, dir, "a.txt", "hand-edited")
	writeFile(t, dir, "user-scratch.txt", "mine")

	if clean, _ := r.IsClean(); clean {
		t.Fatal("a modified tracked file should make IsClean false")
	}

	snap, err := r.SnapshotDirtyTracked("snapshot", DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if snap == "" {
		t.Fatal("SnapshotDirtyTracked should have committed the tracked edit")
	}
	// The edit is now preserved in history (recoverable), worktree clean of TRACKED
	// changes; the untracked user file is untouched and uncommitted.
	if clean, _ := r.IsClean(); !clean {
		t.Fatal("after snapshot, tracked changes should be clean")
	}
	if got := readFile(t, dir, "a.txt"); got != "hand-edited" {
		t.Fatalf("snapshot lost the edit: a.txt = %q", got)
	}
	if inHead(t, dir, "user-scratch.txt") {
		t.Fatal("untracked user file must NOT be committed by the snapshot")
	}
	if _, err := os.Stat(filepath.Join(dir, "user-scratch.txt")); err != nil {
		t.Fatalf("untracked user file disturbed: %v", err)
	}

	// Nothing dirty → no-op.
	again, err := r.SnapshotDirtyTracked("noop", DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if again != "" {
		t.Fatalf("snapshot of a clean tree returned %q, want empty", again)
	}
	// An untracked-only worktree is still "clean" for our purposes.
	writeFile(t, dir, "another-scratch.txt", "also mine")
	if clean, _ := r.IsClean(); !clean {
		t.Fatal("untracked-only changes should still count as clean")
	}
}

func TestHasMultipleCheckpoints(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "a.txt", "1", "c1")
	if multi, _ := r.HasMultipleCheckpoints(); multi {
		t.Fatal("one commit should not be multiple checkpoints")
	}
	commitFile(t, r, dir, "a.txt", "2", "c2")
	if multi, _ := r.HasMultipleCheckpoints(); !multi {
		t.Fatal("two commits should be multiple checkpoints")
	}
}

func TestNowSeamPinsCommitTime(t *testing.T) {
	testenv.RequireContainer(t)
	orig := now
	defer func() { now = orig }()
	fixed := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }

	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "a.txt", "1", "c1")
	repo, _ := gogit.PlainOpen(dir)
	ref, _ := repo.Head()
	c, _ := repo.CommitObject(ref.Hash())
	if !c.Author.When.Equal(fixed) {
		t.Fatalf("commit time = %v, want pinned %v", c.Author.When, fixed)
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
