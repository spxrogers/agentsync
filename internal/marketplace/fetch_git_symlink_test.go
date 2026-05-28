package marketplace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRejectEscapingSymlinks covers the helper that closes the git-fetcher
// symlink hole: a cloned plugin repo containing a symlink whose target ESCAPES
// the tree (which the lexical component-path containment check cannot catch)
// must be refused, while a clean tree and an IN-TREE symlink pass and symlinks
// inside .git are ignored.
func TestRejectEscapingSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rejectEscapingSymlinks(root); err != nil {
		t.Fatalf("clean tree should pass: %v", err)
	}

	// An in-tree symlink (the superpowers AGENTS.md -> CLAUDE.md case) is allowed.
	if err := os.Symlink("../ok.txt", filepath.Join(root, "sub", "intree")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	if err := rejectEscapingSymlinks(root); err != nil {
		t.Fatalf("in-tree symlink should pass: %v", err)
	}

	// Symlinks under .git are skipped (never projected).
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(gitDir, "evil")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	if err := rejectEscapingSymlinks(root); err != nil {
		t.Fatalf(".git symlinks must be skipped: %v", err)
	}

	// A symlink escaping the tree is rejected.
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "sub", "leak")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	if err := rejectEscapingSymlinks(root); err == nil {
		t.Fatal("expected rejection for a symlink escaping the tree")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error should name the symlink; got: %v", err)
	}
}
