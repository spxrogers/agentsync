package marketplace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRejectSymlinks covers the helper that closes the git-fetcher symlink
// hole: a cloned plugin repo containing a symlink (which the lexical
// component-path containment check cannot catch) must be refused, while a
// clean tree passes and symlinks inside .git are ignored.
func TestRejectSymlinks(t *testing.T) {
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
	if err := rejectSymlinks(root); err != nil {
		t.Fatalf("clean tree should pass: %v", err)
	}

	// Symlinks under .git are skipped (never projected).
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(gitDir, "evil")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	if err := rejectSymlinks(root); err != nil {
		t.Fatalf(".git symlinks must be skipped: %v", err)
	}

	// A symlink anywhere in the tracked tree is rejected.
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "sub", "leak")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	if err := rejectSymlinks(root); err == nil {
		t.Fatal("expected rejection for a symlink in the tree")
	}
}
