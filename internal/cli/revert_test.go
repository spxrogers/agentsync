package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agit "github.com/spxrogers/agentsync/internal/git"
)

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
	if len(before) != 2 {
		t.Fatalf("want 2 checkpoints before revert, got %d", len(before))
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
	// Append-only: a new commit on top (3 total), originals still reachable.
	after, _ := repo.Log(0)
	if len(after) != 3 {
		t.Fatalf("want 3 checkpoints after revert (append-only), got %d", len(after))
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
	oldest := cps[len(cps)-1].Hash // the v1 checkpoint (first apply)

	if out, err := runCLI(t, env, "revert", "claude", "--to", oldest); err != nil {
		t.Fatalf("revert --to: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(destSkill); !strings.Contains(string(b), "v1") {
		t.Fatalf("after revert --to oldest, dest skill should hold v1:\n%s", b)
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

// TestRevert_FoldedSharedDirNoted is the regression for the folded-dir revert
// ISSUE: ~/.claude/skills is versioned as part of ~/.claude (de-nested), so
// `revert opencode` must NOT error trying to open a repo at ~/.claude/skills — it
// notes the dir is covered by a parent and moves on.
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
	_ = os.WriteFile(srcSkill, []byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644)
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

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
}

// TestRevert_SharedDirWarnsBlastRadius asserts the user-facing warning that
// reverting a shared dir (~/.agents/skills, owned by codex + warp) rolls back the
// other agent's files too.
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
	apply("v1")
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
