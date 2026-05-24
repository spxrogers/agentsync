package marketplace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExtractSubdir_RejectsTraversal is the regression for the git-subdir
// path-traversal hole: a marketplace plugin entry can set `path` to a
// traversal sequence ("../../etc"), which filepath.Join resolves OUTSIDE the
// clone. Without a containment guard, copyDir would slurp arbitrary host
// files into the plugin cache (then project them into agent config). The
// extractor must refuse and leave the clone untouched.
func TestExtractSubdir_RejectsTraversal(t *testing.T) {
	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	if err := os.MkdirAll(filepath.Join(clone, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "real", "ok.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A sibling "secret" dir OUTSIDE the clone, holding a sensitive file.
	secret := filepath.Join(parent, "secret")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secret, "id_rsa"), []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := extractSubdir(clone, "../secret"); err == nil {
		t.Fatal("expected traversal subdir to be rejected, got nil error")
	}

	// The clone must not have been replaced with the external secret dir.
	if _, err := os.Stat(filepath.Join(clone, "id_rsa")); err == nil {
		t.Fatal("external file id_rsa was copied into the clone — traversal succeeded")
	}
}

// TestExtractSubdir_RejectsSymlinkEscape is the regression for the
// symlink-escape exfiltration: go-git materializes committed symlinks, so a
// git-subdir plugin can ship its `path` subdir (or an intermediate component)
// as a symlink pointing off-clone (e.g. /etc, ~/.ssh). The lexical containment
// check passes (fullSub is still lexically under the clone), but os.Stat and
// copyDir FOLLOW the link and slurp the off-clone target into the cache — and
// from there into agent config. The extractor must resolve symlinks and refuse
// anything that escapes the clone root.
func TestExtractSubdir_RejectsSymlinkEscape(t *testing.T) {
	cases := []struct {
		name    string
		subPath string
		// link maps a clone-relative symlink path -> its (absolute) target.
		link func(clone, secret string) (linkPath, target string)
	}{
		{
			name:    "subdir is a symlink",
			subPath: "sub",
			link:    func(clone, secret string) (string, string) { return filepath.Join(clone, "sub"), secret },
		},
		{
			name:    "intermediate component is a symlink",
			subPath: "link/inner",
			link:    func(clone, secret string) (string, string) { return filepath.Join(clone, "link"), secret },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			clone := filepath.Join(parent, "clone")
			if err := os.MkdirAll(clone, 0o755); err != nil {
				t.Fatal(err)
			}
			// A sensitive tree OUTSIDE the clone, with an "inner" subdir so the
			// intermediate-component case resolves to a real directory.
			secret := filepath.Join(parent, "secret")
			if err := os.MkdirAll(filepath.Join(secret, "inner"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(secret, "id_rsa"), []byte("PRIVATE KEY"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(secret, "inner", "id_rsa"), []byte("PRIVATE KEY"), 0o600); err != nil {
				t.Fatal(err)
			}
			linkPath, target := tc.link(clone, secret)
			if err := os.Symlink(target, linkPath); err != nil {
				t.Skipf("symlinks unsupported: %v", err)
			}

			if err := extractSubdir(clone, tc.subPath); err == nil {
				t.Fatal("expected symlink-escape subdir to be rejected, got nil error")
			}
			if _, err := os.Stat(filepath.Join(clone, "id_rsa")); err == nil {
				t.Fatal("off-clone file id_rsa was copied into the clone — symlink-escape succeeded")
			}
		})
	}
}

// TestExtractSubdir_HappyPath confirms the guard does not break a legitimate
// in-tree subdirectory extraction.
func TestExtractSubdir_HappyPath(t *testing.T) {
	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	if err := os.MkdirAll(filepath.Join(clone, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "skills", "a.md"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractSubdir(clone, "skills"); err != nil {
		t.Fatalf("legitimate subdir extraction failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone, "a.md")); err != nil {
		t.Fatalf("subdir contents not promoted to clone root: %v", err)
	}
}
