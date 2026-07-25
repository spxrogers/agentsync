package marketplace

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// RelativeFetcher copies a local directory tree into the destination.
// The src.Relative field is treated as an absolute path or a path relative
// to the caller's working directory; callers should resolve it to absolute
// before invoking Fetch.
//
// If src.RootDir is non-empty, the resolved Relative path is required to
// be contained within RootDir — this prevents a malicious marketplace
// entry from setting `"source": "../../../../etc"` and copying arbitrary
// host files into the plugin cache. Containment is checked both textually
// and on symlink-resolved paths, so neither a symlinked source path nor a
// symlinked intermediate directory can point the copy outside the root; a
// symlink that RESOLVES inside the root is allowed (mirroring the git
// fetcher's in-tree-symlink policy). A rootless call (RootDir == "") is a
// user-named local path with no boundary to defend and is followed as-is.
// Entries INSIDE the copied tree are always symlink-refused by copyDir.
type RelativeFetcher struct{}

// Fetch copies src.Relative (a local directory) into into.
func (f *RelativeFetcher) Fetch(src Source, into string) (FetchResult, error) {
	srcPath := src.Relative
	if srcPath == "" {
		return FetchResult{}, fmt.Errorf("relative fetcher: empty Relative path")
	}

	abs, err := filepath.Abs(srcPath)
	if err != nil {
		return FetchResult{}, fmt.Errorf("relative fetcher: abs %s: %w", srcPath, err)
	}
	abs = filepath.Clean(abs)

	var rootAbs string
	if src.RootDir != "" {
		root, err := filepath.Abs(src.RootDir)
		if err != nil {
			return FetchResult{}, fmt.Errorf("relative fetcher: abs root %s: %w", src.RootDir, err)
		}
		root = filepath.Clean(root)
		if !pathContains(root, abs) {
			return FetchResult{}, fmt.Errorf("relative fetcher: source %q escapes marketplace root %q", abs, root)
		}
		rootAbs = root
	}

	// os.Stat (following a symlinked source) is deliberate. A ROOTLESS call
	// (RootDir == "") is a user-named path — `marketplace add ~/dev/mp`, which
	// may legitimately be a symlink in a dotfiles layout; there is no trust
	// boundary to defend, so it is followed exactly as it always was. A ROOTED
	// call is governed by the resolved-containment check below instead: a
	// symlink is fine as long as it RESOLVES inside the root (mirroring the git
	// fetcher's in-tree-symlink policy), and an escaping one is refused there.
	// Existence is checked before the resolve step so a missing source still
	// reports the familiar "stat …: no such file or directory".
	info, err := os.Stat(abs)
	if err != nil {
		return FetchResult{}, fmt.Errorf("relative fetcher: stat %s: %w", abs, err)
	}
	if !info.IsDir() {
		return FetchResult{}, fmt.Errorf("relative fetcher: %s is not a directory", abs)
	}

	copySrc := abs
	if src.RootDir != "" {
		// The containment check above is purely textual, so a symlink UNDER the
		// root defeats it: with root/a → /etc, the path root/a/b is "contained"
		// while the tree actually copied lives outside; likewise a symlinked
		// leaf. Re-check containment on fully-resolved paths. The walk-time
		// rejection in copyDir cannot catch either case — it only sees entries
		// INSIDE the tree being copied, never the components leading to it.
		resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
		if err != nil {
			return FetchResult{}, fmt.Errorf("relative fetcher: resolve root %s: %w", rootAbs, err)
		}
		resolvedAbs, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return FetchResult{}, fmt.Errorf("relative fetcher: resolve %s: %w", abs, err)
		}
		if !pathContains(resolvedRoot, resolvedAbs) {
			return FetchResult{}, fmt.Errorf("relative fetcher: source %q escapes marketplace root %q after resolving symlinks", abs, rootAbs)
		}
		// Copy from the RESOLVED path: it has no symlink components left, so a
		// link swapped in between the check above and the walk cannot redirect
		// the copy (the check-to-copy race the unresolved path would leave
		// open). A rootless copy keeps the user-named path as-is.
		copySrc = resolvedAbs
	}

	if err := copyDir(copySrc, into); err != nil {
		from := copySrc
		if copySrc != abs {
			// Name the user-recognizable path too — the resolved spelling alone
			// can be surprising in an error about a path the user never typed.
			from = fmt.Sprintf("%s (resolved from %s)", copySrc, abs)
		}
		return FetchResult{}, fmt.Errorf("relative fetcher: copy %s → %s: %w", from, into, err)
	}
	return FetchResult{}, nil
}

// pathContains reports whether child is the same path as parent or sits
// inside it. Both inputs must already be absolute and Clean'd.
func pathContains(parent, child string) bool {
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	// Cross-volume on Windows; "../foo" anywhere; treat as escape.
	if rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	if len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
		return false
	}
	return true
}

// copyDir recursively copies src directory tree into dst, creating dst if needed.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		// Reject symlinks rather than dereferencing them. copyFile does
		// os.Open (which follows the link), so a marketplace tree with a
		// symlink to /etc/passwd (or a dir symlink escaping the root) would
		// otherwise have its target's content copied into the plugin cache
		// and projected into agent config. The RootDir containment check
		// only validates the top-level source path, not links discovered
		// during the walk — mirror the npm fetcher's loud reject.
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("relative fetcher: %s is a symlink (refusing — marketplace trees must contain only regular files and directories)", srcPath)
		}
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile copies a single file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
