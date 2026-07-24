package git

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
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

// TestHasNestedRepoBelowFollowsSymlink pins the D3 symlink-detection contract from
// issue #149: a foreign repo reachable ONLY through a symlinked subdir below dir is
// still detected (plain filepath.WalkDir does not follow symlinks). The real repo lives
// OUTSIDE dir so the symlink is the sole path to its .git — a walk that ignored
// symlinks would report false.
func TestHasNestedRepoBelowFollowsSymlink(t *testing.T) {
	testenv.RequireContainer(t)

	t.Run("symlink to a foreign repo is detected", func(t *testing.T) {
		root := t.TempDir()
		external := t.TempDir() // the real repo, OUTSIDE root
		if _, err := gogit.PlainInit(external, false); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(root, "plugins")); err != nil {
			t.Skipf("symlinks unavailable on this platform: %v", err)
		}
		if got, err := HasNestedRepoBelow(root); err != nil || !got {
			t.Fatalf("HasNestedRepoBelow via symlink = (%v,%v), want (true,nil)", got, err)
		}
	})

	t.Run("symlink to a plain non-repo dir is not a false positive", func(t *testing.T) {
		root := t.TempDir()
		external := t.TempDir()
		writeFile(t, external, "a/b.txt", "x") // a real dir, but no .git anywhere in it
		if err := os.Symlink(external, filepath.Join(root, "plugins")); err != nil {
			t.Skipf("symlinks unavailable on this platform: %v", err)
		}
		if got, err := HasNestedRepoBelow(root); err != nil || got {
			t.Fatalf("HasNestedRepoBelow(symlink to non-repo) = (%v,%v), want (false,nil)", got, err)
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

// TestIsAncestorOfHead pins the checkpoint-membership helper revert's --to
// validation rides on: HEAD itself and its ancestors are in, a hash outside the
// history is out.
func TestIsAncestorOfHead(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	h1 := commitFile(t, r, dir, "a.txt", "1", "c1")
	h2 := commitFile(t, r, dir, "a.txt", "2", "c2")

	tests := []struct {
		name, hash string
		want       bool
	}{
		{name: "HEAD itself", hash: h2, want: true},
		{name: "an ancestor checkpoint", hash: h1, want: true},
		{name: "a hash outside the history", hash: "0123456789abcdef0123456789abcdef01234567", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.IsAncestorOfHead(tt.hash)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("IsAncestorOfHead(%s) = %v, want %v", tt.hash, got, tt.want)
			}
		})
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

	h, _, err := r.Restore("HEAD~2", "agentsync revert: test", DefaultIdentity)
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
	noop, _, err := r.Restore("HEAD", "noop", DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if noop != "" {
		t.Fatalf("restore to HEAD returned %q, want empty (no-op)", noop)
	}
}

// TestRestore_PreservesUntrackedFiles is the regression guard for issue #128:
// Restore must NOT delete the user's own untracked/gitignored files (go-git's
// HardReset would have; the delta-apply must not). Fails against the old
// HardReset implementation, which enumerates and removes them.
func TestRestore_PreservesUntrackedFiles(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "a.txt", "v1", "apply 1") // HEAD~1 target
	commitFile(t, r, dir, "a.txt", "v2", "apply 2") // HEAD

	// Drop files the user owns that git is NOT versioning: a plain untracked file,
	// plus a .gitignore and a matching ignored file (invisible to go-git status).
	const notesBody = "my personal notes — do not delete\n"
	const scratchBody = "ephemeral scratch log line\n"
	writeFile(t, dir, "MY-PERSONAL-NOTES.txt", notesBody)
	writeFile(t, dir, ".gitignore", "scratch.log\n")
	writeFile(t, dir, "scratch.log", scratchBody)

	// UntrackedPaths should surface the plain untracked files (not the ignored one).
	untracked, err := r.UntrackedPaths()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(untracked, "MY-PERSONAL-NOTES.txt") {
		t.Fatalf("UntrackedPaths = %v, want it to include MY-PERSONAL-NOTES.txt", untracked)
	}
	if slices.Contains(untracked, "scratch.log") {
		t.Fatalf("UntrackedPaths = %v, should NOT list the gitignored scratch.log", untracked)
	}

	h, _, err := r.Restore("HEAD~1", "agentsync revert: preserve untracked", DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if h == "" {
		t.Fatal("Restore returned empty hash")
	}

	// The tracked restore still happened.
	if got := readFile(t, dir, "a.txt"); got != "v1" {
		t.Fatalf("after restore a.txt = %q, want v1", got)
	}
	// Both of the user's own files survive byte-for-byte.
	if got := readFile(t, dir, "MY-PERSONAL-NOTES.txt"); got != notesBody {
		t.Fatalf("untracked file was mutated/deleted: got %q", got)
	}
	if got := readFile(t, dir, "scratch.log"); got != scratchBody {
		t.Fatalf("gitignored file was mutated/deleted: got %q", got)
	}

	// Neither user file was swept into the revert commit's tree — they stay untracked.
	tree := headTreePaths(t, r)
	for _, p := range []string{"MY-PERSONAL-NOTES.txt", "scratch.log"} {
		if _, ok := tree[p]; ok {
			t.Fatalf("revert commit tree unexpectedly contains %s: %v", p, tree)
		}
	}
	if _, ok := tree["a.txt"]; !ok {
		t.Fatalf("revert commit tree missing the tracked a.txt: %v", tree)
	}
}

// headTreePaths returns the set of file paths in the repo's current HEAD tree.
func headTreePaths(t *testing.T, r *Repo) map[string]bool {
	t.Helper()
	log, err := r.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(log) == 0 {
		t.Fatalf("Log returned no commits")
	}
	repo, err := gogit.PlainOpen(r.Dir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c, err := repo.CommitObject(plumbing.NewHash(log[0].Hash))
	if err != nil {
		t.Fatalf("commit object: %v", err)
	}
	tree, err := c.Tree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	out := map[string]bool{}
	_ = tree.Files().ForEach(func(f *object.File) error {
		out[f.Name] = true
		return nil
	})
	return out
}

// commitFileMode writes rel with the given mode, stages it, and commits, returning
// the hash. Used to exercise restoreFileFromTree's mode preservation (notably the
// exec bit). An explicit Chmod pins the mode even when rel already exists (a
// content edit) — os.WriteFile only honors perms when it creates the file.
func commitFileMode(t *testing.T, r *Repo, dir, rel, content string, mode os.FileMode, msg string) string {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	if err := os.Chmod(abs, mode); err != nil {
		t.Fatalf("chmod %s: %v", rel, err)
	}
	if err := r.Stage([]string{rel}); err != nil {
		t.Fatalf("stage %s: %v", rel, err)
	}
	h, err := r.CommitStaged(msg, DefaultIdentity)
	if err != nil {
		t.Fatalf("commit %s: %v", rel, err)
	}
	return h
}

// assertPerm fails unless <dir>/<rel> has exactly the given permission bits.
func assertPerm(t *testing.T, dir, rel string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("stat %s: %v", rel, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s perm = %04o, want %04o", rel, got, want)
	}
}

// TestRestore_PrunesEmptiedDirs is the regression for the stray-empty-dir gap:
// go-git's wt.Remove deletes only the file, so a revert that deletes skill/deep/
// SKILL.md used to leave skill/ and skill/deep/ behind as empty dirs (unlike
// `git reset --hard`). The delete pass must now prune each deleted file's emptied
// parent chain — while NEVER removing a dir that still has entries: an untracked
// user file in the same dir keeps that dir (and its ancestors) alive.
func TestRestore_PrunesEmptiedDirs(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	// c1 (target): only keep.txt exists — no skill/, no shared/.
	commitFile(t, r, dir, "keep.txt", "k", "c1: baseline")
	// c2..c3 (HEAD): a nested managed file plus a managed file in a dir the user
	// also drops their own file into.
	commitFile(t, r, dir, "skill/deep/SKILL.md", "body", "c2: nested skill")
	commitFile(t, r, dir, "shared/managed.txt", "m", "c3: shared managed")
	const userBody = "mine — keep this dir alive\n"
	writeFile(t, dir, "shared/user.txt", userBody)

	// Revert to c1: the delta deletes skill/deep/SKILL.md and shared/managed.txt.
	h, _, err := r.Restore("HEAD~2", "agentsync revert: prune", DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if h == "" {
		t.Fatal("Restore returned empty hash")
	}
	// The fully-emptied chain is pruned: neither skill/deep nor skill remains.
	for _, rel := range []string{"skill/deep", "skill"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("emptied dir %q should be pruned after the revert delete pass; stat err = %v", rel, err)
		}
	}
	// The dir holding the user's untracked file survives, file intact, managed
	// sibling gone.
	if info, err := os.Stat(filepath.Join(dir, "shared")); err != nil || !info.IsDir() {
		t.Fatalf("shared/ holds an untracked user file and must survive; stat = (%v, %v)", info, err)
	}
	if got := readFile(t, dir, "shared/user.txt"); got != userBody {
		t.Fatalf("untracked user file mutated: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared", "managed.txt")); !os.IsNotExist(err) {
		t.Fatalf("managed shared/managed.txt should be deleted by the revert; stat err = %v", err)
	}
}

// TestRestore_PreservesFileMode backs restoreFileFromTree's documented
// mode-preservation contract (notably the exec bit): a revert that re-creates a
// tracked 0o755 script must restore it executable, not as 0o644. Without an
// enforcing test, a refactor that dropped the mode handling would silently strip
// +x from restored skill scripts on every revert while the suite stayed green.
func TestRestore_PreservesFileMode(t *testing.T) {
	testenv.RequireContainer(t)

	t.Run("create path", func(t *testing.T) {
		dir := t.TempDir()
		r, err := Init(dir)
		if err != nil {
			t.Fatal(err)
		}
		commitFileMode(t, r, dir, "run.sh", "#!/bin/sh\necho v1\n", 0o755, "add script") // HEAD~1
		// Remove the script in a second checkpoint so reverting RE-CREATES it fresh
		// (a fresh OpenFile, where the target mode must be set).
		if err := os.Remove(filepath.Join(dir, "run.sh")); err != nil {
			t.Fatal(err)
		}
		if _, err := r.StageTrackedDeletions(); err != nil {
			t.Fatal(err)
		}
		if _, err := r.CommitStaged("remove script", DefaultIdentity); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.Restore("HEAD~1", "agentsync revert: restore script", DefaultIdentity); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, dir, "run.sh"); got != "#!/bin/sh\necho v1\n" {
			t.Fatalf("run.sh content = %q, want v1", got)
		}
		assertPerm(t, dir, "run.sh", 0o755)
	})

	t.Run("modify path", func(t *testing.T) {
		dir := t.TempDir()
		r, err := Init(dir)
		if err != nil {
			t.Fatal(err)
		}
		// run.sh stays a file across both checkpoints but flips 0o755 -> 0o644, so
		// reverting exercises the MODIFY path: restoreFileFromTree must Remove the
		// existing 0o644 file before OpenFile so the target 0o755 mode takes hold
		// (OpenFile only honors perm on creation).
		commitFileMode(t, r, dir, "run.sh", "#!/bin/sh\necho v1\n", 0o755, "exec v1") // HEAD~1
		commitFileMode(t, r, dir, "run.sh", "#!/bin/sh\necho v2\n", 0o644, "nonexec v2")
		if _, _, err := r.Restore("HEAD~1", "agentsync revert: restore exec bit", DefaultIdentity); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, dir, "run.sh"); got != "#!/bin/sh\necho v1\n" {
			t.Fatalf("run.sh content = %q, want v1", got)
		}
		assertPerm(t, dir, "run.sh", 0o755)
	})
}

// TestRestore_FileDirTransition exercises a path that is a FILE in the target
// checkpoint but a DIRECTORY prefix in HEAD (foo vs foo/bar). Restore must apply
// the delete (foo/bar) before the create (foo) so the now-empty dir can be
// replaced by the file; the pre-fix single-pass sorted order created "foo" first
// and aborted with "directory not empty".
func TestRestore_FileDirTransition(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	// c1 (target, HEAD~1): foo is a regular file.
	commitFile(t, r, dir, "foo", "iamfile", "c1: foo is a file")
	// c2 (HEAD): foo becomes a directory (remove the file, add foo/bar).
	if err := os.Remove(filepath.Join(dir, "foo")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StageTrackedDeletions(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "foo/bar", "iamdir")
	if err := r.Stage([]string{"foo/bar"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitStaged("c2: foo is a dir", DefaultIdentity); err != nil {
		t.Fatal(err)
	}
	// Revert to c1: delete foo/bar, then create the file foo over the empty dir.
	if _, _, err := r.Restore("HEAD~1", "agentsync revert: dir->file", DefaultIdentity); err != nil {
		t.Fatalf("Restore across a file<->dir transition should not error: %v", err)
	}
	if got := readFile(t, dir, "foo"); got != "iamfile" {
		t.Fatalf("foo = %q, want the restored file content", got)
	}
	// foo is now a regular file, so foo/bar cannot exist (stat yields ENOENT or
	// ENOTDIR); either way a nil error would mean the dir entry survived.
	if _, err := os.Stat(filepath.Join(dir, "foo", "bar")); err == nil {
		t.Fatal("foo/bar should be gone after reverting to the file checkpoint")
	}
}

// TestRestore_FileToDirTransition is the reverse of TestRestore_FileDirTransition:
// the target checkpoint has foo as a DIRECTORY (foo/bar) while HEAD has foo as a
// regular file. The delete of the file foo must run before restoreFileFromTree's
// MkdirAll("foo"), which would otherwise fail over the still-present file. It
// fails against the pre-fix single-pass sorted order (create foo/bar first).
func TestRestore_FileToDirTransition(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	// c1 (target, HEAD~1): foo is a directory containing foo/bar.
	commitFile(t, r, dir, "foo/bar", "iamdir", "c1: foo is a dir")
	// c2 (HEAD): foo becomes a regular file (remove the dir, add the file foo).
	if err := os.RemoveAll(filepath.Join(dir, "foo")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StageTrackedDeletions(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "foo", "iamfile")
	if err := r.Stage([]string{"foo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitStaged("c2: foo is a file", DefaultIdentity); err != nil {
		t.Fatal(err)
	}
	// Revert to c1: delete the file foo, then MkdirAll("foo") + create foo/bar.
	if _, _, err := r.Restore("HEAD~1", "agentsync revert: file->dir", DefaultIdentity); err != nil {
		t.Fatalf("Restore across a file->dir transition should not error: %v", err)
	}
	if got := readFile(t, dir, "foo/bar"); got != "iamdir" {
		t.Fatalf("foo/bar = %q, want the restored dir content", got)
	}
}

// TestRestore_UntrackedSiblingBlocksFileReplacement pins the fail-safe contract
// for the one case Restore genuinely cannot complete: the target turns a directory
// into a file, but the user left an UNTRACKED file inside that directory. Restore
// must refuse (never delete the untracked file) with an actionable error, and the
// untracked file must survive.
func TestRestore_UntrackedSiblingBlocksFileReplacement(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	// c1 (target): foo is a regular file.
	commitFile(t, r, dir, "foo", "iamfile", "c1: foo is a file")
	// c2 (HEAD): foo becomes a directory (foo/bar tracked).
	if err := os.Remove(filepath.Join(dir, "foo")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StageTrackedDeletions(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "foo/bar", "iamdir")
	if err := r.Stage([]string{"foo/bar"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitStaged("c2: foo is a dir", DefaultIdentity); err != nil {
		t.Fatal(err)
	}
	// The user drops an untracked scratch file inside dir foo.
	const scratch = "keep me — untracked\n"
	writeFile(t, dir, "foo/scratch.txt", scratch)

	// Reverting to c1 would need to replace dir foo with a file, but foo still
	// holds the untracked scratch file — Restore must refuse, not delete it.
	_, _, err = r.Restore("HEAD~1", "agentsync revert: blocked", DefaultIdentity)
	if err == nil {
		t.Fatal("Restore should refuse when an untracked file blocks a dir->file replacement")
	}
	if !strings.Contains(err.Error(), "agentsync does not manage") {
		t.Fatalf("error should point at the unmanaged files; got: %v", err)
	}
	if got := readFile(t, dir, "foo/scratch.txt"); got != scratch {
		t.Fatalf("untracked scratch file must survive the refused revert, got %q", got)
	}
	// All-or-nothing: the pre-flight refuses BEFORE any delete, so the tracked
	// foo/bar must still be present (no half-applied worktree mutation).
	if got := readFile(t, dir, "foo/bar"); got != "iamdir" {
		t.Fatalf("refusal must be all-or-nothing: tracked foo/bar was mutated, got %q", got)
	}
}

// TestRestore_GitignoredSiblingBlocksFileReplacement proves the pre-flight is
// filesystem-based, not git-status-based: a GITIGNORED file (which git status
// omits) inside a dir being replaced by a file must still block the revert
// all-or-nothing, since #128 preserves gitignored files equally.
func TestRestore_GitignoredSiblingBlocksFileReplacement(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "foo", "iamfile", "c1: foo is a file") // target
	// c2: foo becomes a dir (foo/bar tracked) with a .gitignore for *.log under it.
	if err := os.Remove(filepath.Join(dir, "foo")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StageTrackedDeletions(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "foo/bar", "iamdir")
	writeFile(t, dir, ".gitignore", "foo/*.log\n")
	if err := r.Stage([]string{"foo/bar", ".gitignore"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitStaged("c2: foo is a dir", DefaultIdentity); err != nil {
		t.Fatal(err)
	}
	const scratch = "gitignored — keep me\n"
	writeFile(t, dir, "foo/debug.log", scratch) // gitignored; git status omits it

	if _, _, err := r.Restore("HEAD~1", "agentsync revert: blocked by gitignored", DefaultIdentity); err == nil {
		t.Fatal("Restore should refuse when a gitignored file blocks a dir->file replacement")
	} else if !strings.Contains(err.Error(), "agentsync does not manage") {
		t.Fatalf("error should point at the unmanaged files; got: %v", err)
	}
	if got := readFile(t, dir, "foo/debug.log"); got != scratch {
		t.Fatalf("gitignored file must survive the refused revert, got %q", got)
	}
	if got := readFile(t, dir, "foo/bar"); got != "iamdir" {
		t.Fatalf("refusal must be all-or-nothing: tracked foo/bar was mutated, got %q", got)
	}
}

// TestRestore_ParentFileCollisionBlocked covers the reverse structural conflict:
// creating a target path whose ancestor is an existing untracked FILE (MkdirAll
// would fail over it). The pre-flight must refuse before mutating and preserve the
// untracked file.
func TestRestore_ParentFileCollisionBlocked(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "a/b", "iamdir", "c1: a is a dir")
	commitFile(t, r, dir, "keep", "k", "c2: add keep")
	// c3 (HEAD): remove a/b so "a" no longer exists in the tree.
	if err := os.RemoveAll(filepath.Join(dir, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StageTrackedDeletions(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitStaged("c3: remove a/b", DefaultIdentity); err != nil {
		t.Fatal(err)
	}
	// The user drops an untracked FILE named exactly "a".
	const scratch = "untracked file named a\n"
	writeFile(t, dir, "a", scratch)

	// Revert to c2 (has a/b): creating a/b needs MkdirAll("a") over the untracked
	// file "a" — the pre-flight must refuse before mutating, preserving "a".
	if _, _, err := r.Restore("HEAD~1", "agentsync revert: parent collision", DefaultIdentity); err == nil {
		t.Fatal("Restore should refuse when an untracked file blocks creating a parent dir")
	} else if !strings.Contains(err.Error(), "does not manage") {
		t.Fatalf("error should point at the unmanaged parent; got: %v", err)
	}
	if got := readFile(t, dir, "a"); got != scratch {
		t.Fatalf("untracked file 'a' must survive the refused revert, got %q", got)
	}
}

// TestRestore_CreatePathCollidesWithUntrackedFile pins the untouched-files promise
// for a create-path collision: agentsync manages X, a later apply removes X, the
// user drops their own untracked file at path X. Reverting to a checkpoint that has
// X must refuse rather than silently overwrite + commit over the user's file.
func TestRestore_CreatePathCollidesWithUntrackedFile(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "keep", "k", "c1")
	commitFile(t, r, dir, "X", "managed-content", "c2: add managed X") // HEAD~1 has X
	if err := os.Remove(filepath.Join(dir, "X")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StageTrackedDeletions(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitStaged("c3: remove X", DefaultIdentity); err != nil {
		t.Fatal(err)
	}
	// The user drops their OWN untracked file at path X.
	const user = "my own notes at X\n"
	writeFile(t, dir, "X", user)

	// Reverting to c2 (has managed X) is a create of X — but X is now the user's
	// untracked file. Restore must refuse, not overwrite it.
	if _, _, err := r.Restore("HEAD~1", "agentsync revert: create collision", DefaultIdentity); err == nil {
		t.Fatal("Restore should refuse to overwrite an untracked file at a create path")
	} else if !strings.Contains(err.Error(), "a file agentsync does not manage already exists") {
		t.Fatalf("error should name the create-collision; got: %v", err)
	}
	if got := readFile(t, dir, "X"); got != user {
		t.Fatalf("user's untracked file at X must survive, got %q", got)
	}
}

// TestRestore_DanglingSymlinkAtCreatePathBlocked closes the pre-flight's Stat
// blind spot: a DANGLING untracked symlink at a path the restore will create
// resolves to nothing under os.Stat, so it matched neither refusal arm and
// restoreFileFromTree silently removed the user's symlink and wrote the managed
// file over it. With os.Lstat the link is seen as itself and refused like every
// other create-path conflict — all-or-nothing, before anything is mutated. (A
// symlink to an EXISTING file at a create path was already refused; that arm now
// covers both.)
func TestRestore_DanglingSymlinkAtCreatePathBlocked(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "a.txt", "v1", "c1")
	commitFile(t, r, dir, "X", "managed-content", "c2: add managed X") // target has X
	// c3 (HEAD): remove X and bump a.txt, so reverting to c2 is
	// {create X, modify a.txt}.
	if err := os.Remove(filepath.Join(dir, "X")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StageTrackedDeletions(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "a.txt", "v2")
	if err := r.Stage([]string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitStaged("c3: remove X, bump a.txt", DefaultIdentity); err != nil {
		t.Fatal(err)
	}
	// The user drops a DANGLING symlink at the create path X.
	linkTarget := filepath.Join(dir, "no-such-target")
	if err := os.Symlink(linkTarget, filepath.Join(dir, "X")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	_, _, err = r.Restore("HEAD~1", "agentsync revert: dangling symlink", DefaultIdentity)
	if err == nil {
		t.Fatal("Restore should refuse a dangling symlink at a create path")
	}
	if !strings.Contains(err.Error(), "agentsync does not manage") {
		t.Fatalf("error should be the unmanaged-entry refusal; got: %v", err)
	}
	// The user's symlink survives, still pointing where they aimed it.
	if got, lerr := os.Readlink(filepath.Join(dir, "X")); lerr != nil || got != linkTarget {
		t.Fatalf("dangling symlink must survive the refused revert: target=%q err=%v", got, lerr)
	}
	// All-or-nothing: the refusal fires in the pre-flight, so the OTHER change in
	// the delta (the a.txt modify) was not applied either.
	if got := readFile(t, dir, "a.txt"); got != "v2" {
		t.Fatalf("refusal must be all-or-nothing: a.txt = %q, want the unreverted v2", got)
	}
}

// blobAt returns the content of rel in the tree of commit hash.
func blobAt(t *testing.T, dir, hash, rel string) string {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.CommitObject(plumbing.NewHash(hash))
	if err != nil {
		t.Fatalf("commit object %s: %v", hash, err)
	}
	tree, err := c.Tree()
	if err != nil {
		t.Fatalf("tree of %s: %v", hash, err)
	}
	f, err := tree.File(rel)
	if err != nil {
		t.Fatalf("file %s in commit %s: %v", rel, hash, err)
	}
	s, err := f.Contents()
	if err != nil {
		t.Fatalf("contents of %s: %v", rel, err)
	}
	return s
}

// TestRestoreSnapshotsDirtyTracked is the engine-level regression for issue #146's D1:
// Restore itself (called DIRECTLY, no CLI wrapper) must preserve uncommitted edits to
// already-TRACKED files by snapshotting them into history BEFORE the delta-apply — the
// worktree-safety promise now holds for every caller, not just revert.go. It also
// guards the boundary with #128: an untracked scratch file is left byte-for-byte alone.
// Everything is anchored to the on-disk filesystem / commit trees, not a return value.
func TestRestoreSnapshotsDirtyTracked(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, r, dir, "a.txt", "v1", "apply 1") // HEAD~1 target
	commitFile(t, r, dir, "a.txt", "v2", "apply 2") // HEAD

	// Hand-edit the tracked file on disk WITHOUT committing, and drop an untracked file.
	const handEdit = "hand-edited-after-apply"
	const scratchBody = "my scratch — keep me\n"
	writeFile(t, dir, "a.txt", handEdit)
	writeFile(t, dir, "scratch.txt", scratchBody)

	// Call Restore directly. It must snapshot the dirty tracked edit first, then restore
	// a.txt to the HEAD~1 checkpoint.
	revert, snap, err := r.Restore("HEAD~1", "agentsync revert: engine", DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if revert == "" {
		t.Fatal("Restore returned an empty revert hash")
	}
	if snap == "" {
		t.Fatal("Restore must snapshot the uncommitted tracked edit and return its hash (D1 safety)")
	}

	// On-disk: the worktree now matches the target checkpoint.
	if got := readFile(t, dir, "a.txt"); got != "v1" {
		t.Fatalf("after restore a.txt = %q, want v1 (target checkpoint)", got)
	}
	// Recoverable from history: the snapshot commit's tree carries the hand-edit verbatim.
	if got := blobAt(t, dir, snap, "a.txt"); got != handEdit {
		t.Fatalf("snapshot %s should preserve the hand-edit; a.txt blob = %q, want %q", snap, got, handEdit)
	}
	// Boundary with #128: the untracked scratch file is untouched and never versioned.
	if got := readFile(t, dir, "scratch.txt"); got != scratchBody {
		t.Fatalf("untracked scratch file was mutated/deleted by Restore: %q", got)
	}
	if untracked, _ := r.UntrackedPaths(); !slices.Contains(untracked, "scratch.txt") {
		t.Fatalf("scratch.txt should still be untracked after Restore; UntrackedPaths=%v", untracked)
	}
}

// TestRestoreFailureHint pins the recovery-hint formatting for issue #146's D2: a
// failure once the delta-apply has begun (after the snapshot advanced HEAD) must surface
// an error naming BOTH the snapshot commit and the pre-revert HEAD (orig), so the user
// can recover their preserved edits. A genuine mid-delta-apply failure needs filesystem
// conditions that are impractical to force deterministically in-container (the pre-flight
// already refuses every structural conflict; the remaining triggers are disk-full /
// EACCES mid-write), so we assert the pure error-wrapping helper directly.
func TestRestoreFailureHint(t *testing.T) {
	orig := "0123456789abcdef0123456789abcdef01234567"
	snap := "89abcdef0123456789abcdef0123456789abcdef"
	inner := errors.New("write a.txt: disk full")

	t.Run("with snapshot names orig and snapshot", func(t *testing.T) {
		err := restoreFailureHint("/dest/.claude", orig, snap, inner)
		msg := err.Error()
		for _, want := range []string{Short(snap), Short(orig), "reset --hard", "disk full"} {
			if !strings.Contains(msg, want) {
				t.Errorf("hint %q missing %q", msg, want)
			}
		}
		if !errors.Is(err, inner) {
			t.Error("hint must wrap the inner error (errors.Is)")
		}
	})
	t.Run("without snapshot still names orig", func(t *testing.T) {
		err := restoreFailureHint("/dest/.claude", orig, "", inner)
		msg := err.Error()
		if !strings.Contains(msg, Short(orig)) || !strings.Contains(msg, "reset --hard") {
			t.Errorf("hint %q should name orig + a reset --hard recovery command", msg)
		}
		if strings.Contains(msg, Short(snap)) {
			t.Errorf("hint without a snapshot must not name a snapshot hash: %q", msg)
		}
	})
}

// TestShort pins the single shared 7-char hex abbreviator (formerly the three
// identical shortStr/shortHash/shortRef copies).
func TestShort(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{name: "full 40-char hex", in: "0123456789abcdef0123456789abcdef01234567", want: "0123456"},
		{name: "exactly 7", in: "abcdef0", want: "abcdef0"},
		{name: "shorter than 7", in: "abc", want: "abc"},
		{name: "empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Short(tt.in); got != tt.want {
				t.Fatalf("Short(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestDetect_ErrorReturnsForeign pins the fail-safe: on a non-ErrRepositoryNotExists
// open error Detect returns StateForeign ("leave it alone"), never the init-eligible
// StateUntracked, so a caller reading State before err cannot init into an unreadable
// repo. The happy path still reports (StateUntracked, nil) for a plain dir.
func TestDetect_ErrorReturnsForeign(t *testing.T) {
	testenv.RequireContainer(t)

	t.Run("malformed .git returns foreign + error", func(t *testing.T) {
		dir := t.TempDir()
		// A .git FILE with no "gitdir:" prefix opens with an error that is NOT
		// ErrRepositoryNotExists (go-git: ".git file has no gitdir: prefix").
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a gitfile\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := Detect(dir)
		if err == nil {
			t.Fatalf("expected an open error for a malformed .git, got nil (state %v)", st)
		}
		if st != StateForeign {
			t.Fatalf("state on error = %v, want StateForeign (fail-safe)", st)
		}
	})

	t.Run("plain dir returns untracked + nil", func(t *testing.T) {
		st, err := Detect(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if st != StateUntracked {
			t.Fatalf("state = %v, want StateUntracked", st)
		}
	})
}

// TestPlan_ModifyAndUnknown asserts a real HEAD↔target diff that MODIFIES a file
// yields Kind "modify" via the explicit merkletrie.Modify case (not the former
// default-arm masquerade). The "unknown" default is defensive only — merkletrie's
// Action() returns exactly Insert/Delete/Modify, so it is unreachable via a real
// diff and cannot be exercised here without fabricating an invalid change.
func TestPlan_ModifyAndUnknown(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	repo, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, repo, dir, "f.txt", "A\n", "one")
	commitFile(t, repo, dir, "f.txt", "B\n", "two") // same path, new content → a MODIFY

	_, changes, err := repo.Plan("HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	var got *FileChange
	for i := range changes {
		if changes[i].Path == "f.txt" {
			got = &changes[i]
		}
	}
	if got == nil {
		t.Fatalf("f.txt not in plan: %+v", changes)
	}
	if got.Kind != "modify" {
		t.Fatalf("Kind = %q, want \"modify\" (explicit merkletrie.Modify case)", got.Kind)
	}
}

// contentInHead returns the content of rel as committed in the repo's HEAD tree.
func contentInHead(t *testing.T, dir, rel string) string {
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
	f, err := tree.File(rel)
	if err != nil {
		t.Fatalf("%s not found in HEAD tree: %v", rel, err)
	}
	content, err := f.Contents()
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// TestSnapshotDirtyTracked_CleartextCapture pins the two-sided contract of the
// pre-destructive safety snapshot (issue #167): it commits dirty TRACKED files
// verbatim — including a rendered dest file whose ${secret:…} was resolved to
// CLEARTEXT, the accepted at-rest exposure that is only tolerable because these
// repos are never pushed — while NEVER sweeping in the user's untracked scratch
// files (isUntracked honored). Unlike TestSnapshotDirtyTrackedAndIsClean it reads
// the committed blob back to prove the cleartext was actually captured, and pins
// `now` for a deterministic commit.
func TestSnapshotDirtyTracked_CleartextCapture(t *testing.T) {
	testenv.RequireContainer(t)
	orig := now
	defer func() { now = orig }()
	now = func() time.Time { return time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC) }

	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A tracked rendered file that starts life with a benign placeholder value.
	commitFile(t, r, dir, "rendered.json", `{"token":"placeholder"}`, "baseline apply")

	// The user (or a re-apply) rewrites the tracked file to hold a resolved
	// cleartext secret, and separately drops an untracked scratch file.
	const cleartext = `{"token":"ghp_SUPER_SECRET_CLEARTEXT"}`
	writeFile(t, dir, "rendered.json", cleartext)
	writeFile(t, dir, "user-scratch.txt", "my own notes — do not commit")

	snap, err := r.SnapshotDirtyTracked("safety snapshot", DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if snap == "" {
		t.Fatal("SnapshotDirtyTracked should have committed the dirty tracked file")
	}

	// (a) The modified tracked file is captured, cleartext and all — this is the
	//     accepted design (local-only history), not a leak the snapshot must block.
	if !inHead(t, dir, "rendered.json") {
		t.Fatal("the modified tracked file must be captured in the snapshot commit")
	}
	if got := contentInHead(t, dir, "rendered.json"); got != cleartext {
		t.Fatalf("snapshot captured %q, want the verbatim cleartext %q", got, cleartext)
	}

	// (b) The untracked scratch file is NEVER swept into the snapshot commit...
	if inHead(t, dir, "user-scratch.txt") {
		t.Fatal("an untracked user file must NOT be committed by the snapshot")
	}
	// ...and is left untouched on disk.
	if got := readFile(t, dir, "user-scratch.txt"); got != "my own notes — do not commit" {
		t.Fatalf("untracked scratch file was disturbed: %q", got)
	}

	// The pinned clock makes the snapshot commit deterministic.
	repo, _ := gogit.PlainOpen(dir)
	ref, _ := repo.Head()
	c, _ := repo.CommitObject(ref.Hash())
	if want := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC); !c.Author.When.Equal(want) {
		t.Errorf("snapshot commit time = %v, want pinned %v", c.Author.When, want)
	}
}

func TestStage_WriteDriven(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Baseline checkpoint: three tracked files, exec.sh starting NON-executable.
	commitFile(t, r, dir, "same.txt", "unchanged-bytes\n", "baseline same")
	commitFileMode(t, r, dir, "exec.sh", "#!/bin/sh\necho hi\n", 0o644, "baseline exec")
	commitFile(t, r, dir, "changed.txt", "v1\n", "baseline changed")

	// A second apply WRITES a set of files regardless of whether the bytes moved:
	//   (a) same.txt    — rewritten with byte-identical content.
	//   (b) exec.sh     — mode-only change (chmod +x), same bytes.
	//   (c) changed.txt — genuinely new content (keeps the checkpoint non-empty).
	writeFile(t, dir, "same.txt", "unchanged-bytes\n") // identical bytes
	if err := os.Chmod(filepath.Join(dir, "exec.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "changed.txt", "v2\n")

	written := []string{"same.txt", "exec.sh", "changed.txt"}
	if err := r.Stage(written); err != nil {
		t.Fatalf("stage the written set: %v", err)
	}
	h, err := r.CommitStaged("write-driven checkpoint", DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if h == "" {
		t.Fatal("checkpoint should be non-empty (changed.txt moved)")
	}

	// Every member of the written set is present in the checkpoint tree.
	tree := headTreePaths(t, r)
	for _, p := range written {
		if !tree[p] {
			t.Fatalf("written path %q missing from checkpoint tree %v", p, tree)
		}
	}
	// (a) the byte-identical rewrite survives with its bytes intact.
	if got := blobAt(t, dir, h, "same.txt"); got != "unchanged-bytes\n" {
		t.Fatalf("same.txt in checkpoint = %q, want the unchanged bytes", got)
	}
	// (b) the mode-only change is captured: the tree entry is executable, proving
	//     the exec bit rode into the checkpoint though not one content byte changed.
	if mode := treeEntryMode(t, r, "exec.sh"); mode != filemode.Executable {
		t.Fatalf("exec.sh checkpoint mode = %v, want Executable (mode-only change captured)", mode)
	}
	// (c) the genuinely-changed file carries its new bytes.
	if got := blobAt(t, dir, h, "changed.txt"); got != "v2\n" {
		t.Fatalf("changed.txt in checkpoint = %q, want v2", got)
	}
}

// treeEntryMode returns the git filemode of rel in the repo's current HEAD tree.
func treeEntryMode(t *testing.T, r *Repo, rel string) filemode.FileMode {
	t.Helper()
	repo, err := gogit.PlainOpen(r.Dir())
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
	e, err := tree.FindEntry(rel)
	if err != nil {
		t.Fatalf("find %s in HEAD tree: %v", rel, err)
	}
	return e.Mode
}
