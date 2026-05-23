package render

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/state"
)

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
