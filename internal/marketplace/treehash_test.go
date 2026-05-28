package marketplace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/marketplace"
)

// TestPluginTreeHash_HashesSymlinkTarget locks in that the pin hashes a cached
// symlink by its target path rather than refusing it: an in-tree symlink (now
// allowed by the git fetcher) must not brick pin computation/verification, and
// swapping the target must still change the pin so a tamper can't hide.
func TestPluginTreeHash_HashesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("real.txt", link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	fs := afero.NewOsFs()
	h1, err := marketplace.PluginTreeHash(fs, dir)
	if err != nil {
		t.Fatalf("hashing a tree with an in-tree symlink should succeed: %v", err)
	}

	// Re-hashing the unchanged tree is stable.
	if h2, err := marketplace.PluginTreeHash(fs, dir); err != nil || h2 != h1 {
		t.Fatalf("hash not stable: h1=%s h2=%s err=%v", h1, h2, err)
	}

	// Swapping the link target changes the pin.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other.txt", link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	h3, err := marketplace.PluginTreeHash(fs, dir)
	if err != nil {
		t.Fatalf("re-hash after target swap: %v", err)
	}
	if h3 == h1 {
		t.Fatal("changing the symlink target must change the pin")
	}
}
