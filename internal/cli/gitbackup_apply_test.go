package cli_test

import (
	"os"
	"path/filepath"
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
