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
// and on symlink-resolved paths, and the source path itself is rejected if
// it is a symlink — the same escapes-by-symlink policy copyDir applies to
// every entry inside the tree.
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

	if src.RootDir != "" {
		root, err := filepath.Abs(src.RootDir)
		if err != nil {
			return FetchResult{}, fmt.Errorf("relative fetcher: abs root %s: %w", src.RootDir, err)
		}
		root = filepath.Clean(root)
		if !pathContains(root, abs) {
			return FetchResult{}, fmt.Errorf("relative fetcher: source %q escapes marketplace root %q", abs, root)
		}
		// The check above is purely textual, so a symlink UNDER the root defeats
		// it: with root/a → /etc, the path root/a/b is "contained" while the tree
		// actually copied lives outside. Re-check containment on fully-resolved
		// paths (EvalSymlinks also resolves a symlinked leaf, closing the same
		// hole for the source path itself). The walk-time rejection in copyDir
		// cannot catch either case — it only sees entries INSIDE the tree being
		// copied, never the components leading to it.
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return FetchResult{}, fmt.Errorf("relative fetcher: resolve root %s: %w", root, err)
		}
		resolvedAbs, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return FetchResult{}, fmt.Errorf("relative fetcher: resolve %s: %w", abs, err)
		}
		if !pathContains(resolvedRoot, resolvedAbs) {
			return FetchResult{}, fmt.Errorf("relative fetcher: source %q escapes marketplace root %q after resolving symlinks", abs, root)
		}
	}

	// Lstat, not Stat: the source path itself must not be a symlink — the same
	// policy copyDir enforces for every entry inside the tree. A rootless call
	// (RootDir == "") has no containment re-check above, so following a symlink
	// here would copy an arbitrary target the caller never named.
	info, err := os.Lstat(abs)
	if err != nil {
		return FetchResult{}, fmt.Errorf("relative fetcher: stat %s: %w", abs, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return FetchResult{}, fmt.Errorf("relative fetcher: %s is a symlink (refusing — marketplace trees must contain only regular files and directories)", abs)
	}
	if !info.IsDir() {
		return FetchResult{}, fmt.Errorf("relative fetcher: %s is not a directory", abs)
	}

	if err := copyDir(abs, into); err != nil {
		return FetchResult{}, fmt.Errorf("relative fetcher: copy %s → %s: %w", abs, into, err)
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
