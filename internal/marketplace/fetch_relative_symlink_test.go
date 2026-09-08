package marketplace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/marketplace"
)

// TestRelativeFetcher_CopiesInTreeSymlinkEntry is the regression for a local-path
// marketplace being STRICTER than the same repo fetched over git: copyDir refused
// every symlink inside the tree, while the git fetcher's rejectEscapingSymlinks
// permits one that resolves in-tree. A repo that keeps one component tree and
// links the per-agent views at it (.claude/skills/x -> .agents/skills/x) was
// therefore registrable as `github:` but refused as a local path — the shape a
// private repo is reduced to, since the git fetcher passes no credentials.
func TestRelativeFetcher_CopiesInTreeSymlinkEntry(t *testing.T) {
	src := t.TempDir()
	skill := filepath.Join(src, ".agents", "skills", "shadcn")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("shadcn"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Directory link, relative — the shape a real repo uses.
	if err := os.Symlink(
		filepath.Join("..", "..", ".agents", "skills", "shadcn"),
		filepath.Join(src, ".claude", "skills", "shadcn"),
	); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	// File link, absolute — must be dereferenced without rewriting.
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("readme"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "README.md"), filepath.Join(src, "AGENTS.md")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	dst := t.TempDir()
	source := marketplace.Source{Relative: src}
	if _, err := marketplace.Dispatch(source).Fetch(source, dst); err != nil {
		t.Fatalf("in-tree symlinks must be copied, got: %v", err)
	}

	linked := filepath.Join(dst, ".claude", "skills", "shadcn", "SKILL.md")
	data, err := os.ReadFile(linked)
	if err != nil {
		t.Fatalf("symlinked skill dir not copied: %v", err)
	}
	if string(data) != "shadcn" {
		t.Errorf("%s content = %q, want %q", linked, data, "shadcn")
	}
	if data, err := os.ReadFile(filepath.Join(dst, "AGENTS.md")); err != nil {
		t.Fatalf("symlinked file not copied: %v", err)
	} else if string(data) != "readme" {
		t.Errorf("AGENTS.md content = %q, want %q", data, "readme")
	}

	// The cache must contain no symlinks: dereferencing, not recreating, is what
	// keeps anything reading the cache later from being redirected by a link.
	if err := filepath.WalkDir(dst, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			t.Errorf("cache contains a symlink: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRelativeFetcher_RefusesBadSymlinkEntry pins the three shapes that must
// still be refused now that an in-tree link is copied.
func TestRelativeFetcher_RefusesBadSymlinkEntry(t *testing.T) {
	tests := []struct {
		name    string
		build   func(t *testing.T, src string)
		wantErr string
	}{
		{
			// The original hole: a link whose target is a host file outside the
			// tree. copyFile follows links, so this would leak the target.
			name: "escapes the tree",
			build: func(t *testing.T, src string) {
				outside := filepath.Join(t.TempDir(), "secret.txt")
				if err := os.WriteFile(outside, []byte("TOP SECRET HOST FILE"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(src, "leak.txt")); err != nil {
					t.Skipf("symlink unsupported on this platform: %v", err)
				}
			},
			wantErr: "outside the marketplace tree",
		},
		{
			// Fail closed, as the git fetcher does: an unresolvable link is
			// refused rather than guessed.
			name: "dangling",
			build: func(t *testing.T, src string) {
				if err := os.Symlink(filepath.Join(src, "nope"), filepath.Join(src, "dangling.txt")); err != nil {
					t.Skipf("symlink unsupported on this platform: %v", err)
				}
			},
			wantErr: "cannot resolve symlink",
		},
		{
			// No counterpart in the git fetcher, which preserves links instead
			// of following them: dereferencing this would recurse forever.
			name: "points at its own ancestor",
			build: func(t *testing.T, src string) {
				sub := filepath.Join(src, "sub")
				if err := os.MkdirAll(sub, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(src, filepath.Join(sub, "loop")); err != nil {
					t.Skipf("symlink unsupported on this platform: %v", err)
				}
			},
			wantErr: "own ancestor",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := t.TempDir()
			if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("ok"), 0o644); err != nil {
				t.Fatal(err)
			}
			tc.build(t, src)

			dst := t.TempDir()
			source := marketplace.Source{Relative: src}
			_, err := marketplace.Dispatch(source).Fetch(source, dst)
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
			if _, statErr := os.Stat(filepath.Join(dst, "leak.txt")); statErr == nil {
				t.Fatal("symlink target leaked into the cache")
			}
		})
	}
}
