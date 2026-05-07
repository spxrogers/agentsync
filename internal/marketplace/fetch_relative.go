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
type RelativeFetcher struct{}

// Fetch copies src.Relative (a local directory) into into.
func (f *RelativeFetcher) Fetch(src Source, into string) (FetchResult, error) {
	srcPath := src.Relative
	if srcPath == "" {
		return FetchResult{}, fmt.Errorf("relative fetcher: empty Relative path")
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return FetchResult{}, fmt.Errorf("relative fetcher: stat %s: %w", srcPath, err)
	}
	if !info.IsDir() {
		return FetchResult{}, fmt.Errorf("relative fetcher: %s is not a directory", srcPath)
	}

	if err := copyDir(srcPath, into); err != nil {
		return FetchResult{}, fmt.Errorf("relative fetcher: copy %s → %s: %w", srcPath, into, err)
	}
	return FetchResult{}, nil
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
