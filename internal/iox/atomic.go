// Package iox provides atomic file IO and file-locking primitives used by
// agentsync's apply pipeline.
package iox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// AllowSymlinkDestEnv lets callers opt in to AtomicWrite following an
// existing symlink rather than refusing. Default behaviour refuses so a
// chezmoi-style dotfile symlink at the destination is not silently
// replaced with a regular file.
const AllowSymlinkDestEnv = "AGENTSYNC_ALLOW_SYMLINK_DEST"

// ErrSymlinkDest is returned when dest is an existing symlink and the
// caller has not set AGENTSYNC_ALLOW_SYMLINK_DEST=1.
var ErrSymlinkDest = errors.New("destination is a symlink")

// AtomicWrite writes data to dest using a three-phase approach: write to a
// sibling .agentsync.tmp file (always created mode 0o600 so cleartext
// payloads — secrets, age TOML — never sit world-readable in the destination
// directory between create and rename), fsync it, rename(2) into place,
// chmod to the caller-requested mode, then fsync the parent directory. If
// the process crashes between phases, the destination is either the old
// content (rename did not run) or the new content (rename ran). Never
// partial.
//
// If dest is an existing symlink, AtomicWrite refuses unless
// AGENTSYNC_ALLOW_SYMLINK_DEST=1. Replacing the symlink with a regular file
// via rename would silently break a chezmoi/Stow setup where the user's
// real source lives behind the link. With the env set, dest is resolved
// through the link first so the underlying file is updated in place and
// the symlink itself is preserved.
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
	resolved, err := resolveSymlinkDest(dest)
	if err != nil {
		return err
	}
	dest = resolved
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", dest, err)
	}
	tmp := dest + ".agentsync.tmp"

	// Always create tmp with 0o600 so a payload containing freshly-resolved
	// secrets does not briefly sit world-readable in the destination
	// directory. We chmod to the requested mode AFTER rename.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
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
	// Chmod after rename to honor the caller's requested mode. The umask
	// would otherwise tighten the initial 0o600 create above. Best-effort
	// on Windows (no-op via os.Chmod which only writes the read-only bit).
	if err := os.Chmod(dest, mode); err != nil {
		// Surface but don't unwind — the content is on disk correctly;
		// only the permission bits are off.
		return fmt.Errorf("chmod %s to %o: %w", dest, mode, err)
	}
	if err := fsyncDir(parent); err != nil {
		// Fsync of the directory is durability-only — the rename already
		// succeeded. Surface as a wrapped error so callers can decide;
		// state.Save in particular treats the file as written.
		return fmt.Errorf("fsync parent %s: %w", parent, err)
	}
	return nil
}

// resolveSymlinkDest inspects dest. If it is a symlink, it is either
// rejected (default) or resolved (when AGENTSYNC_ALLOW_SYMLINK_DEST=1)
// so the underlying file is updated in place. This preserves the symlink
// itself, which is what chezmoi/Stow users want — a rename onto the link
// would replace it with a regular file.
func resolveSymlinkDest(dest string) (string, error) {
	info, err := os.Lstat(dest)
	if err != nil {
		// Missing dest is fine — caller is about to create it.
		return dest, nil
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return dest, nil
	}
	if os.Getenv(AllowSymlinkDestEnv) != "1" {
		target, _ := os.Readlink(dest)
		return "", fmt.Errorf("%w: %s -> %s (refusing to replace the symlink; set %s=1 to write through it)",
			ErrSymlinkDest, dest, target, AllowSymlinkDestEnv)
	}
	// Follow the link and update the file it points at in place. We use
	// EvalSymlinks so chains and relative targets resolve correctly.
	resolved, err := filepath.EvalSymlinks(dest)
	if err != nil {
		return "", fmt.Errorf("resolve symlink %s: %w", dest, err)
	}
	return resolved, nil
}

// fsyncDir opens dir and calls Sync. On Windows, opening a directory for
// fsync is not supported; we silently skip there. Linux/macOS errors are
// surfaced because a sync failure means the rename may not survive a
// power loss.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		// On Windows, opening a directory for sync is not supported.
		// We can't tell that apart from a real permission error without
		// runtime.GOOS — treat any open error as best-effort since the
		// rename has already succeeded.
		return nil
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems (some Windows fs, procfs) reject Sync on
		// directories. Best-effort.
		return nil
	}
	return nil
}
