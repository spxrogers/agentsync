package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	agit "github.com/spxrogers/agentsync/internal/git"
)

// orphanCommitIn records a real commit in the repo at dir and then rewinds the
// branch to its previous tip — leaving the commit's object in the store
// (resolvable by hash) but NOT an ancestor of HEAD, i.e. a commit outside the
// checkpoint history. The worktree and index are reset back, so the repo is left
// exactly as found.
func orphanCommitIn(t *testing.T, dir string) string {
	t.Helper()
	gr, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := gr.Head()
	if err != nil {
		t.Fatal(err)
	}
	wt, err := gr.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("off-history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("stray.txt"); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "test", Email: "test@example.com", When: time.Unix(1_700_000_000, 0).UTC()}
	h, err := wt.Commit("stray lineage", &gogit.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.Reset(&gogit.ResetOptions{Mode: gogit.HardReset, Commit: head.Hash()}); err != nil {
		t.Fatal(err)
	}
	return h.String()
}

// setupGitBackedClaude inits an agentsync home with claude + git backup on, then
// applies twice (skill body "v1" then "v2") so ~/.claude has two checkpoints.
// Returns tmp, env, and the destination skill path.
func setupGitBackedClaude(t *testing.T) (string, map[string]string, string) {
	t.Helper()
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n[destination_directory_git_backup]\nmode = \"on\"\n")
	_ = f.Close()

	srcSkill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(srcSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	apply := func(body string) {
		if err := os.WriteFile(srcSkill, []byte("---\nname: demo\ndescription: d\n---\n"+body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runCLI(t, env, "apply"); err != nil {
			t.Fatalf("apply: %v\n%s", err, out)
		}
	}
	apply("v1")
	apply("v2")

	destSkill := filepath.Join(tmp, ".claude", "skills", "demo", "SKILL.md")
	if b, _ := os.ReadFile(destSkill); !strings.Contains(string(b), "v2") {
		t.Fatalf("precondition: dest skill should hold v2:\n%s", b)
	}
	return tmp, env, destSkill
}

func TestRevert_DefaultUndoesLastApply(t *testing.T) {
	tmp, env, destSkill := setupGitBackedClaude(t)
	claude := filepath.Join(tmp, ".claude")
	repo, _ := agit.Open(claude)
	before, _ := repo.Log(0)
	// Two applies produce a pre-apply baseline (first apply) + two apply checkpoints
	// (issue #143): 3 commits.
	if len(before) != 3 {
		t.Fatalf("want 3 commits before revert (baseline + v1 + v2), got %d", len(before))
	}

	out, err := runCLI(t, env, "revert", "claude")
	if err != nil {
		t.Fatalf("revert: %v\n%s", err, out)
	}
	// Dest skill is back to v1.
	if b, _ := os.ReadFile(destSkill); !strings.Contains(string(b), "v1") || strings.Contains(string(b), "v2") {
		t.Fatalf("after revert dest skill should hold v1:\n%s", b)
	}
	// Out-of-sync notice printed.
	if !strings.Contains(out, "out of sync") {
		t.Errorf("expected out-of-sync notice, got:\n%s", out)
	}
	// Append-only: a new commit on top, originals still reachable.
	after, _ := repo.Log(0)
	if len(after) != len(before)+1 {
		t.Fatalf("want one more commit after revert (append-only): %d -> %d", len(before), len(after))
	}
}

func TestRevert_DryRunWritesNothing(t *testing.T) {
	tmp, env, destSkill := setupGitBackedClaude(t)
	claude := filepath.Join(tmp, ".claude")
	repo, _ := agit.Open(claude)
	before, _ := repo.Log(0)

	out, err := runCLI(t, env, "revert", "claude", "--dry-run")
	if err != nil {
		t.Fatalf("revert --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got:\n%s", out)
	}
	// Nothing changed on disk or in history.
	if b, _ := os.ReadFile(destSkill); !strings.Contains(string(b), "v2") {
		t.Fatalf("dry-run mutated the dest skill:\n%s", b)
	}
	after, _ := repo.Log(0)
	if len(after) != len(before) {
		t.Fatalf("dry-run recorded a commit: %d -> %d", len(before), len(after))
	}
}

func TestRevert_ToSpecificCheckpoint(t *testing.T) {
	tmp, env, destSkill := setupGitBackedClaude(t)
	claude := filepath.Join(tmp, ".claude")
	repo, _ := agit.Open(claude)
	cps, _ := repo.Log(0)
	// cps (newest→oldest) is [v2, v1, baseline]; the v1 apply checkpoint is the
	// second-oldest — the oldest is now the pre-apply baseline (notice only, issue
	// #143), which would restore an empty dir rather than v1.
	v1 := cps[len(cps)-2].Hash

	if out, err := runCLI(t, env, "revert", "claude", "--to", v1); err != nil {
		t.Fatalf("revert --to: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(destSkill); !strings.Contains(string(b), "v1") {
		t.Fatalf("after revert --to v1, dest skill should hold v1:\n%s", b)
	}
}

func TestRevert_Errors(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	t.Run("conflicting flags", func(t *testing.T) {
		if _, err := runCLI(t, env, "revert", "claude", "--all"); err == nil {
			t.Fatal("expected error for agent + --all")
		}
	})
	t.Run("all plus to", func(t *testing.T) {
		// --to names a checkpoint in ONE repo and cannot span every managed dir, so
		// `revert --all --to <ref>` is rejected up front by the guard in revert.go's
		// RunE (before any repo is touched). A resolvable ref is used to prove the
		// rejection is the flag-combination guard, not a bad-ref resolution error.
		if _, err := runCLI(t, env, "revert", "--all", "--to", "HEAD~1"); err == nil {
			t.Fatal("expected error for --all combined with --to")
		}
	})
	t.Run("non-ancestor to ref rejected", func(t *testing.T) {
		// A commit whose OBJECT exists in the backup repo but which is not an
		// ancestor of HEAD (an orphaned lineage). git.Resolve alone accepts it;
		// revert must refuse it rather than restore to an arbitrary commit with
		// undefined semantics for the append-only checkpoint history.
		tmp2, env2, destSkill := setupGitBackedClaude(t)
		orphan := orphanCommitIn(t, filepath.Join(tmp2, ".claude"))
		out, rerr := runCLI(t, env2, "revert", "claude", "--to", orphan)
		if rerr == nil {
			t.Fatalf("expected revert --to <non-ancestor> to be refused, got:\n%s", out)
		}
		if !strings.Contains(rerr.Error(), "not a checkpoint") || !strings.Contains(rerr.Error(), agit.Short(orphan)) {
			t.Errorf("refusal should name the hash and explain it is not a checkpoint: %v", rerr)
		}
		// All-or-nothing: the refused revert mutated nothing.
		if b, _ := os.ReadFile(destSkill); !strings.Contains(string(b), "v2") {
			t.Fatalf("refused revert must not mutate the dest:\n%s", b)
		}
		// The dry-run preview predicts the same refusal.
		if _, derr := runCLI(t, env2, "revert", "claude", "--dry-run", "--to", orphan); derr == nil {
			t.Error("revert --dry-run must refuse a non-ancestor --to as well")
		}
	})
	t.Run("no target", func(t *testing.T) {
		if _, err := runCLI(t, env, "revert"); err == nil {
			t.Fatal("expected error when neither agent nor --all given")
		}
	})
	t.Run("unknown agent", func(t *testing.T) {
		if _, err := runCLI(t, env, "revert", "bogus"); err == nil {
			t.Fatal("expected error for unknown agent")
		}
	})
	t.Run("unmanaged dir", func(t *testing.T) {
		// claude was never git-backed (no apply with backup) → not an agentsync repo.
		out, err := runCLI(t, env, "revert", "claude")
		if err == nil {
			t.Fatalf("expected error reverting an unmanaged dir, got:\n%s", out)
		}
		if !strings.Contains(err.Error(), "not an agentsync-managed") {
			t.Errorf("error should explain the dir is unmanaged: %v", err)
		}
	})
}

// TestRevert_PreservesUncommittedEdits is the regression for the data-loss finding:
// a hand-edit to a tracked file after the last apply must NOT be silently destroyed
// by revert's hard reset. revert snapshots it first, so it stays recoverable.
func TestRevert_PreservesUncommittedEdits(t *testing.T) {
	tmp, env, destSkill := setupGitBackedClaude(t)
	claude := filepath.Join(tmp, ".claude")

	// Hand-edit the rendered skill after the last apply (uncommitted drift).
	if err := os.WriteFile(destSkill, []byte("HAND-EDITED-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "revert", "claude")
	if err != nil {
		t.Fatalf("revert: %v\n%s", err, out)
	}
	// The revert happened (skill restored to the v1 checkpoint)...
	if b, _ := os.ReadFile(destSkill); !strings.Contains(string(b), "v1") {
		t.Fatalf("revert should restore v1:\n%s", b)
	}
	// ...and the hand-edit was preserved as a snapshot, not lost.
	if !strings.Contains(out, "preserved uncommitted changes") {
		t.Errorf("expected a snapshot notice, got:\n%s", out)
	}
	repo, _ := agit.Open(claude)
	cps, _ := repo.Log(0)
	var snap string
	for _, c := range cps {
		if strings.Contains(c.Subject, "snapshot uncommitted changes") {
			snap = c.Hash
		}
	}
	if snap == "" {
		t.Fatal("no snapshot commit recorded; the hand-edit was lost")
	}
	// Recoverable: reverting to the snapshot restores the hand-edit verbatim.
	if out, err := runCLI(t, env, "revert", "claude", "--to", snap); err != nil {
		t.Fatalf("revert --to snapshot: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(destSkill); string(b) != "HAND-EDITED-CONTENT" {
		t.Fatalf("hand-edit not recoverable from the snapshot: %q", b)
	}
}

// TestRevertSnapshotPrintsCleartextCaution pins issue #126: when revert preserves a
// dirty-tracked snapshot, it cautions that the snapshot may carry cleartext secrets
// into the local-only history (dest files hold secrets resolved to cleartext).
func TestRevertSnapshotPrintsCleartextCaution(t *testing.T) {
	_, env, destSkill := setupGitBackedClaude(t)

	// Uncommitted hand-edit to a tracked dest file → revert snapshots it first.
	if err := os.WriteFile(destSkill, []byte("HAND-EDITED"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "revert", "claude")
	if err != nil {
		t.Fatalf("revert: %v\n%s", err, out)
	}
	if !strings.Contains(out, "preserved uncommitted changes") {
		t.Fatalf("expected the snapshot-preserved notice, got:\n%s", out)
	}
	if !strings.Contains(out, "may contain secrets in cleartext") {
		t.Errorf("expected a cleartext caution alongside the snapshot notice, got:\n%s", out)
	}
}

// TestRevert_FoldedSharedDirNoted is the regression for the folded-dir revert
// ISSUE: ~/.claude/skills is versioned as part of ~/.claude (de-nested), so
// `revert opencode` must NOT error trying to open a repo at ~/.claude/skills — it
// notes the dir is covered by a parent and moves on (leaving the files alone).
// It also proves the note's claim on disk: reverting the PARENT is what rolls the
// folded skill back, byte-for-byte, to the prior checkpoint.
func TestRevert_FoldedSharedDirNoted(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "opencode")
	f, _ := os.OpenFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"), os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("\n[destination_directory_git_backup]\nmode = \"on\"\n")
	_ = f.Close()
	srcSkill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
	_ = os.MkdirAll(filepath.Dir(srcSkill), 0o755)
	apply := func(body string) {
		_ = os.WriteFile(srcSkill, []byte("---\nname: demo\ndescription: d\n---\n"+body+"\n"), 0o644)
		if out, err := runCLI(t, env, "apply"); err != nil {
			t.Fatalf("apply: %v\n%s", err, out)
		}
	}
	foldedSkill := filepath.Join(tmp, ".claude", "skills", "demo", "SKILL.md")
	apply("v1")
	wantV1, err := os.ReadFile(foldedSkill)
	if err != nil {
		t.Fatalf("apply v1 should render the folded skill: %v", err)
	}
	if !strings.Contains(string(wantV1), "v1") {
		t.Fatalf("precondition: captured v1 bytes should contain v1:\n%s", wantV1)
	}
	apply("v2") // ~/.claude (the folding parent) now has two checkpoints

	// ~/.claude/skills exists but has no .git of its own (folded into ~/.claude).
	if _, err := os.Stat(filepath.Join(tmp, ".claude", "skills", ".git")); !os.IsNotExist(err) {
		t.Fatalf("~/.claude/skills should be folded into ~/.claude, not its own repo; err=%v", err)
	}
	out, err := runCLI(t, env, "revert", "opencode")
	if err != nil {
		t.Fatalf("revert opencode must not error on a folded dir: %v\n%s", err, out)
	}
	if !strings.Contains(out, "versioned as part of a parent") {
		t.Errorf("expected the folded-dir note for ~/.claude/skills; got:\n%s", out)
	}
	// The child's revert only NOTES the folding — it must not have rolled the
	// folded dir back itself: the skill still holds the v2 bytes.
	if b, _ := os.ReadFile(foldedSkill); !strings.Contains(string(b), "v2") {
		t.Fatalf("revert opencode must leave the folded dir to the parent; skill:\n%s", b)
	}
	// And the note's claim must be TRUE: reverting the parent (claude) actually
	// rolls the folded shared dir back to the exact v1 bytes.
	if out, err := runCLI(t, env, "revert", "claude"); err != nil {
		t.Fatalf("revert claude: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(foldedSkill); string(got) != string(wantV1) {
		t.Fatalf("revert claude must roll the folded skill back to the v1 bytes:\n got %q\nwant %q", got, wantV1)
	}
}

// TestRevert_PreservesUntrackedUserFiles is the CLI-level regression for issue
// #128: a file the user dropped into a managed dest dir (untracked by agentsync's
// backup) must survive a revert byte-for-byte and never be swept into any
// checkpoint. The old go-git HardReset would have deleted it.
func TestRevert_PreservesUntrackedUserFiles(t *testing.T) {
	tmp, env, destSkill := setupGitBackedClaude(t)
	claude := filepath.Join(tmp, ".claude")

	// The user drops a scratch file into ~/.claude that agentsync never wrote.
	const userBody = "personal scratch — keep me\n"
	userFile := filepath.Join(claude, "MY-NOTES.txt")
	if err := os.WriteFile(userFile, []byte(userBody), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "revert", "claude")
	if err != nil {
		t.Fatalf("revert: %v\n%s", err, out)
	}
	// The revert otherwise succeeded: dest skill back to v1.
	if b, _ := os.ReadFile(destSkill); !strings.Contains(string(b), "v1") || strings.Contains(string(b), "v2") {
		t.Fatalf("after revert dest skill should hold v1:\n%s", b)
	}
	// The user's untracked file survives byte-for-byte.
	if b, _ := os.ReadFile(userFile); string(b) != userBody {
		t.Fatalf("untracked user file was destroyed/mutated by revert: %q", b)
	}
	// It is in NO checkpoint tree — revert never versioned it, so it is still
	// reported untracked after the revert commit.
	repo, _ := agit.Open(claude)
	if untracked, err := repo.UntrackedPaths(); err != nil || !slices.Contains(untracked, "MY-NOTES.txt") {
		t.Fatalf("MY-NOTES.txt should still be untracked; UntrackedPaths=%v err=%v", untracked, err)
	}
}

// TestRevert_DryRunWarnsUntracked verifies --dry-run notes the presence of
// untracked files (left untouched) and writes nothing.
func TestRevert_DryRunWarnsUntracked(t *testing.T) {
	tmp, env, destSkill := setupGitBackedClaude(t)
	claude := filepath.Join(tmp, ".claude")
	repo, _ := agit.Open(claude)
	before, _ := repo.Log(0)

	const userBody = "keep me\n"
	userFile := filepath.Join(claude, "SCRATCH.txt")
	if err := os.WriteFile(userFile, []byte(userBody), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "revert", "claude", "--dry-run")
	if err != nil {
		t.Fatalf("revert --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "untracked file(s) in this dir are left untouched") {
		t.Errorf("expected the untracked-present note, got:\n%s", out)
	}
	// Nothing changed on disk or in history.
	if b, _ := os.ReadFile(destSkill); !strings.Contains(string(b), "v2") {
		t.Fatalf("dry-run mutated the dest skill:\n%s", b)
	}
	if b, _ := os.ReadFile(userFile); string(b) != userBody {
		t.Fatalf("dry-run mutated the untracked file: %q", b)
	}
	after, _ := repo.Log(0)
	if len(after) != len(before) {
		t.Fatalf("dry-run recorded a commit: %d -> %d", len(before), len(after))
	}
}

// TestRevert_SharedDirWarnsBlastRadius asserts the user-facing warning that
// reverting a shared dir (~/.agents/skills, owned by codex + warp) rolls back the
// other agent's files too — and that the rollback the warning describes really
// happened on disk (the shared dest is back to its exact prior-checkpoint bytes).
func TestRevert_SharedDirWarnsBlastRadius(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "codex")
	mustRun(t, env, "agent", "add", "warp")
	f, _ := os.OpenFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"), os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("\n[destination_directory_git_backup]\nmode = \"on\"\n")
	_ = f.Close()
	srcSkill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
	_ = os.MkdirAll(filepath.Dir(srcSkill), 0o755)
	apply := func(body string) {
		_ = os.WriteFile(srcSkill, []byte("---\nname: demo\ndescription: d\n---\n"+body+"\n"), 0o644)
		if out, err := runCLI(t, env, "apply"); err != nil {
			t.Fatalf("apply: %v\n%s", err, out)
		}
	}
	// The shared dest file BOTH owners read (codex and warp render the same path).
	sharedSkill := filepath.Join(tmp, ".agents", "skills", "demo", "SKILL.md")
	apply("v1")
	wantV1, err := os.ReadFile(sharedSkill)
	if err != nil {
		t.Fatalf("apply v1 should render the shared skill: %v", err)
	}
	if !strings.Contains(string(wantV1), "v1") {
		t.Fatalf("precondition: captured v1 bytes should contain v1:\n%s", wantV1)
	}
	apply("v2") // ~/.agents/skills now has two checkpoints

	// ~/.agents/skills is its own repo, shared by codex + warp.
	if st, _ := agit.Detect(filepath.Join(tmp, ".agents", "skills")); st != agit.StateAgentsyncOwned {
		t.Fatalf("shared ~/.agents/skills should be versioned, state=%v", st)
	}
	out, err := runCLI(t, env, "revert", "codex")
	if err != nil {
		t.Fatalf("revert codex: %v\n%s", err, out)
	}
	if !strings.Contains(out, "shared with") || !strings.Contains(out, "warp") {
		t.Fatalf("expected a shared-dir blast-radius warning naming warp; got:\n%s", out)
	}
	// The warning is only honest if the rollback really happened: the shared file
	// — the other owner's (warp's) file too, since both render the same dest — is
	// back to its exact v1 bytes on disk.
	got, err := os.ReadFile(sharedSkill)
	if err != nil {
		t.Fatalf("shared skill missing after revert: %v", err)
	}
	if string(got) != string(wantV1) {
		t.Fatalf("revert codex must roll the shared dir back to the v1 bytes:\n got %q\nwant %q", got, wantV1)
	}
}

// TestRevert_ToAmbiguousMultiRoot: --to is rejected for an agent versioning >1 dir.
func TestRevert_ToAmbiguousMultiRoot(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "codex") // codex versions ~/.codex AND ~/.agents/skills
	_, err := runCLI(t, env, "revert", "codex", "--to", "HEAD~1")
	if err == nil {
		t.Fatal("expected an ambiguity error for --to on a multi-root agent")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should explain --to is ambiguous: %v", err)
	}
}

func TestRevert_All(t *testing.T) {
	_, env, destSkill := setupGitBackedClaude(t)
	if out, err := runCLI(t, env, "revert", "--all"); err != nil {
		t.Fatalf("revert --all: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(destSkill); !strings.Contains(string(b), "v1") {
		t.Fatalf("after revert --all, dest skill should hold v1:\n%s", b)
	}
}
