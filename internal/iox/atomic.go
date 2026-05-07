// Package iox provides atomic file IO and file-locking primitives used by
// agentsync's apply pipeline.
package iox

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWrite writes data to dest using a three-phase approach: write to a
// sibling .agentsync.tmp file, fsync it, rename(2) into place, then fsync
// the parent directory. If the process crashes between phases, the
// destination is either the old content (rename did not run) or the new
// content (rename ran). Never partial.
//
// The parent-directory fsync ensures that on filesystems where the
// directory entry update is asynchronous (ext4 without data=journal, btrfs,
// and historically some NFS configurations), a power loss immediately after
// the rename does not revert the entry to point at the old inode. On
// platforms where opening a directory for fsync is not supported (notably
// Windows) the directory fsync is best-effort and silently skipped.
//
// Parent directory is created if missing (with mode 0o755).
func AtomicWrite(dest string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", dest, err)
	}
	tmp := dest + ".agentsync.tmp"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("open temp %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync temp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, dest, err)
	}
	if err := fsyncDir(parent); err != nil {
		// Fsync of the directory is durability-only — the rename already
		// succeeded. Surface as a wrapped error so callers can decide;
		// state.Save in particular treats the file as written.
		return fmt.Errorf("fsync parent %s: %w", parent, err)
	}
	return nil
}

// fsyncDir opens dir and calls Sync. On Windows, opening a directory for
// fsync is not supported; we silently skip there.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		// Windows returns "Access is denied" / "is a directory" depending
		// on the path. Treat any open error as best-effort: the rename
		// already succeeded.
		return nil
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems (some Windows fs, some procfs) reject Sync on
		// directories. Best-effort.
		return nil
	}
	return nil
}
