package render

import (
	"os"
	"path/filepath"
	"slices"
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
// Since the state key became typed (issue #227) Path is its own field and is
// compared for equality, so this holds by construction — the case stays as a
// regression against anyone reintroducing string matching.
func TestOwnedKeysFor_DisambiguatesColonPaths(t *testing.T) {
	s := state.New()
	// A real owned pointer for path "a".
	s.Keys[state.Key{Agent: "claude", Scope: "user", Path: "a", Pointer: "/legit"}] = state.KeyEntry{SHA256: "x"}
	// A pointer for the DIFFERENT path "a:b". Under the v1 string key its
	// encoding shared the "...:a:" prefix of the shorter path's; the typed key
	// keeps Path in its own field, so this can no longer be misattributed.
	s.Keys[state.Key{Agent: "claude", Scope: "user", Path: "a:b", Pointer: "/realptr"}] = state.KeyEntry{SHA256: "y"}

	got := ownedKeysFor(s, "claude", adapter.ScopeUser, "", "a", "")

	// The assertion is on the EXACT set, not "contains" plus a shape check.
	// ownedKeysFor feeds the key-merge writer's prune, so a pointer it wrongly
	// claims for path "a" is a pointer DELETED out of a file this op does not
	// own — and dropping the Path comparison returns a perfectly well-shaped
	// "/realptr" alongside "/legit", which only an exact-set check catches.
	if want := []string{"/legit"}; !slices.Equal(got, want) {
		t.Fatalf("ownedKeysFor = %v, want exactly %v — a different dest path's pointer must not be claimed", got, want)
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
