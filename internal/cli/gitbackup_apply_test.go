package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agit "github.com/spxrogers/agentsync/internal/git"
)

// TestApply_GitBackupCheckpoint is the end-to-end apply-tail test: with git backup
// enabled, the first apply that writes under ~/.claude initializes a local repo and
// records exactly one checkpoint; a re-apply with no source change records none.
func TestApply_GitBackupCheckpoint(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	// No TTY in tests, so the prompt is unavailable — enable git backup via config.
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n[destination_directory_git_backup]\nmode = \"on\"\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// A skill renders to ~/.claude/skills/demo/SKILL.md — inside the versioned dir
	// (an MCP-only config would only touch ~/.claude.json at $HOME, which is out of
	// scope by design).
	skill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	claude := filepath.Join(tmp, ".claude")
	st, err := agit.Detect(claude)
	if err != nil {
		t.Fatal(err)
	}
	if st != agit.StateAgentsyncOwned {
		t.Fatalf("~/.claude state = %v, want agentsync-owned", st)
	}
	repo, err := agit.Open(claude)
	if err != nil {
		t.Fatal(err)
	}
	cps, err := repo.Log(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cps) != 1 {
		t.Fatalf("want exactly 1 checkpoint after first apply, got %d", len(cps))
	}

	// Re-apply with no source change → no new checkpoint (apply is a no-op).
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("re-apply: %v\n%s", err, out)
	}
	cps2, _ := repo.Log(0)
	if len(cps2) != 1 {
		t.Fatalf("re-apply recorded a checkpoint despite no change: %d commits", len(cps2))
	}
}

// writeSkillSource writes a skill SKILL.md into the canonical source tree.
func writeSkillSource(t *testing.T, tmp, name, body string) {
	t.Helper()
	p := filepath.Join(tmp, ".agentsync", "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\nname: "+name+"\ndescription: d\n---\n"+body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func enableGitBackupOn(t *testing.T, tmp string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n[destination_directory_git_backup]\nmode = \"on\"\n")
	_ = f.Close()
}

// TestApply_GitBackupRecordsDeletion proves a managed-file DELETION is committed to
// the checkpoint (not left as silent worktree drift). If the deletion weren't
// committed, the repo would be dirty (worktree missing a file HEAD still has).
func TestApply_GitBackupRecordsDeletion(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "keep", "k")
	writeSkillSource(t, tmp, "temp", "t")
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 1: %v\n%s", err, out)
	}

	tempDest := filepath.Join(tmp, ".claude", "skills", "temp", "SKILL.md")
	if _, err := os.Stat(tempDest); err != nil {
		t.Fatalf("precondition: temp skill should exist after apply 1: %v", err)
	}

	// Remove the temp skill from source and re-apply → its dest file is deleted.
	if err := os.RemoveAll(filepath.Join(tmp, ".agentsync", "skills", "temp")); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 2: %v\n%s", err, out)
	}
	if _, err := os.Stat(tempDest); !os.IsNotExist(err) {
		t.Fatalf("temp dest skill should be deleted after apply 2, stat err = %v", err)
	}

	repo, err := agit.Open(filepath.Join(tmp, ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	// The deletion was committed iff the worktree matches HEAD (clean).
	clean, err := repo.IsClean()
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Fatal("deletion was not committed to the checkpoint (repo is dirty vs HEAD)")
	}
	cps, _ := repo.Log(0)
	if len(cps) < 2 {
		t.Fatalf("want >=2 checkpoints (add, then delete), got %d", len(cps))
	}
}

// TestApply_GitBackupVersionsSharedSkillsDir proves the shared cross-vendor
// ~/.agents/skills dir is git-versioned (Codex declares it as a version root).
func TestApply_GitBackupVersionsSharedSkillsDir(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "codex")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "demo", "d")
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	shared := filepath.Join(tmp, ".agents", "skills")
	if _, err := os.Stat(filepath.Join(shared, "demo", "SKILL.md")); err != nil {
		t.Fatalf("codex should render the skill to %s: %v", shared, err)
	}
	st, err := agit.Detect(shared)
	if err != nil {
		t.Fatal(err)
	}
	if st != agit.StateAgentsyncOwned {
		t.Fatalf("shared %s state = %v, want agentsync-versioned", shared, st)
	}
}

// TestDoctor_ShowsVersionedRoot checks doctor reports a versioned dir's status.
func TestDoctor_ShowsVersionedRoot(t *testing.T) {
	_, env, _ := setupGitBackedClaude(t)
	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "agentsync-versioned") {
		t.Fatalf("doctor should report ~/.claude as agentsync-versioned; got:\n%s", out)
	}
}

// TestApply_NoGitBackupFlag verifies --no-git-backup skips versioning even when
// mode is "on".
func TestApply_NoGitBackupFlag(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	f, _ := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("\n[destination_directory_git_backup]\nmode = \"on\"\n")
	_ = f.Close()

	skill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
	_ = os.MkdirAll(filepath.Dir(skill), 0o755)
	_ = os.WriteFile(skill, []byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644)

	if out, err := runCLI(t, env, "apply", "--no-git-backup"); err != nil {
		t.Fatalf("apply --no-git-backup: %v\n%s", err, out)
	}
	st, _ := agit.Detect(filepath.Join(tmp, ".claude"))
	if st == agit.StateAgentsyncOwned {
		t.Fatal("--no-git-backup must not create a repo")
	}
}
