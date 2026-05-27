package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/state"
)

// TestPruneEmptySkillDirs_StopsAtSkillsRoot locks the boundary math: emptied
// directories below the skills root are removed, the skills root and any
// non-empty ancestor survive, and the SourceID's depth alone decides where to
// stop (so the function never walks above the skill it owns).
func TestPruneEmptySkillDirs_StopsAtSkillsRoot(t *testing.T) {
	root := t.TempDir()
	skills := filepath.Join(root, ".claude", "skills")
	deep := filepath.Join(skills, "deploy", "scripts")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	runSh := filepath.Join(deep, "run.sh")
	if err := os.WriteFile(runSh, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate the delete, then prune.
	if err := os.Remove(runSh); err != nil {
		t.Fatal(err)
	}
	pruneEmptySkillDirs(runSh, "skills/deploy/scripts/run.sh")

	if _, err := os.Stat(filepath.Join(skills, "deploy")); !os.IsNotExist(err) {
		t.Fatalf("empty skill dir was not pruned: %v", err)
	}
	if _, err := os.Stat(skills); err != nil {
		t.Fatalf("skills root must survive pruning: %v", err)
	}
}

// TestPruneEmptySkillDirs_KeepsNonEmptyDirs ensures a directory that still holds
// other (e.g. untracked) files is never removed.
func TestPruneEmptySkillDirs_KeepsNonEmptyDirs(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".agents", "skills", "deploy")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	removed := filepath.Join(skillDir, "SKILL.md")
	kept := filepath.Join(skillDir, "user-notes.txt")
	if err := os.WriteFile(kept, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	pruneEmptySkillDirs(removed, "skills/deploy/SKILL.md")

	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("non-empty skill dir holding an untracked file was wrongly pruned: %v", err)
	}
}

// TestOwnedKeysFor_DisambiguatesColonPaths is the regression for the colon-
// ambiguity bug PruneStaleState/orphanCleanupOps were hardened against but
// ownedKeysFor was not: a state key whose dest path is a colon-delimited
// string prefix of another path (realistic for a Windows drive path stored
// absolute) must not be claimed as an owned pointer for the shorter path.
// The remainder after the prefix is a JSON pointer, which always begins with
// '/'. ownedKeysFor must reject any remainder that does not.
func TestOwnedKeysFor_DisambiguatesColonPaths(t *testing.T) {
	s := state.New()
	// A real owned pointer for path "a".
	s.Keys["claude:user::a:/legit"] = state.KeyEntry{SHA256: "x"}
	// A pointer for the DIFFERENT path "a:b" — its key shares the "...:a:"
	// prefix but the remainder ("b:/realptr") is not a pointer for "a".
	s.Keys["claude:user::a:b:/realptr"] = state.KeyEntry{SHA256: "y"}

	got := ownedKeysFor(s, "claude", adapter.ScopeUser, "", "a", "")

	for _, k := range got {
		if k == "b:/realptr" {
			t.Fatalf("ownedKeysFor wrongly claimed a foreign path's pointer: %v", got)
		}
		if !strings.HasPrefix(k, "/") {
			t.Fatalf("ownedKeysFor returned a non-pointer remainder %q: %v", k, got)
		}
	}
	found := false
	for _, k := range got {
		if k == "/legit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ownedKeysFor dropped the legitimate pointer /legit: %v", got)
	}
}

// TestBackupPathFor_NeverEscapesRoot asserts that no src — including
// adversarial inputs containing ".." or absolute paths — can place the
// backup destination outside backupRoot.
func TestBackupPathFor_NeverEscapesRoot(t *testing.T) {
	root := filepath.Join("/tmp", "agentsync-backup-root")
	cases := []string{
		"/home/user/.claude/settings.json",
		"/home/user/../../../etc/passwd",
		"../../etc/passwd",
		"/./././tmp/foo",
		"",
		".",
		"..",
	}
	for _, src := range cases {
		dest := backupPathFor(src, root)
		rel, err := filepath.Rel(root, dest)
		if err != nil {
			t.Errorf("backupPathFor(%q): Rel failed: %v", src, err)
			continue
		}
		if strings.HasPrefix(rel, "..") {
			t.Errorf("backupPathFor(%q) = %q escapes root %q (rel=%q)", src, dest, root, rel)
		}
	}
}
