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
// host files into the plugin cache.
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
	}

	info, err := os.Stat(abs)
	if err != nil {
		return FetchResult{}, fmt.Errorf("relative fetcher: stat %s: %w", abs, err)
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
