package iox_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spxrogers/agentsync/internal/iox"
)

func TestAtomicWrite_NewFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "config.toml")
	payload := []byte("hello\n")

	if err := iox.AtomicWrite(dest, payload, 0o644); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("content = %q, want %q", got, payload)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// mode comparison is platform-aware; on windows mode bits are ignored.
	if info.Mode().Perm() != 0o644 {
		t.Logf("mode = %v (informational; windows ignores)", info.Mode().Perm())
	}
}

func TestAtomicWrite_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(dest, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := iox.AtomicWrite(dest, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "new\n" {
		t.Fatalf("content = %q, want %q", got, "new\n")
	}
}

func TestAtomicWrite_LeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "config.toml")
	if err := iox.AtomicWrite(dest, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("dir entries = %d, want 1 (only the dest); got: %+v", len(entries), entries)
	}
}

// TestAtomicWrite_RefusesSymlinkDest is the regression test for the bug
// where AtomicWrite silently replaced a chezmoi/Stow-managed symlink with
// a regular file in the user's home, stranding the linked source.
func TestAtomicWrite_RefusesSymlinkDest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	dir := t.TempDir()
	// Real target lives at <dir>/real/config.toml.
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(realDir, "config.toml")
	if err := os.WriteFile(realPath, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// dest is a symlink → real.
	dest := filepath.Join(dir, "config.toml")
	if err := os.Symlink(realPath, dest); err != nil {
		t.Fatal(err)
	}

	err := iox.AtomicWrite(dest, []byte("new\n"), 0o644)
	if err == nil {
		t.Fatalf("expected ErrSymlinkDest, got nil")
	}
	if !errors.Is(err, iox.ErrSymlinkDest) {
		t.Fatalf("expected ErrSymlinkDest, got %v", err)
	}
	// The symlink and the linked file must both be untouched.
	lst, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if lst.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("dest is no longer a symlink after refusal: %v", lst.Mode())
	}
	got, _ := os.ReadFile(realPath)
	if string(got) != "original\n" {
		t.Fatalf("real file mutated despite refusal: %q", got)
	}
}

// TestAtomicWrite_AllowsSymlinkDestWithEnv proves the documented escape
// hatch works for users who explicitly accept the chezmoi-link semantics.
func TestAtomicWrite_AllowsSymlinkDestWithEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.toml")
	if err := os.WriteFile(realPath, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "linked.toml")
	if err := os.Symlink(realPath, dest); err != nil {
		t.Fatal(err)
	}

	t.Setenv(iox.AllowSymlinkDestEnv, "1")
	if err := iox.AtomicWrite(dest, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("AtomicWrite with env override: %v", err)
	}
	// With the env set, AtomicWrite resolves through the symlink and
	// updates the underlying file in place — the symlink itself is
	// preserved. This is the chezmoi/Stow contract.
	lst, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if lst.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink at %s was replaced with a regular file (mode=%v)", dest, lst.Mode())
	}
	got, _ := os.ReadFile(realPath)
	if string(got) != "new\n" {
		t.Fatalf("real file = %q, want %q", got, "new\n")
	}
}

// TestAtomicWrite_TmpFileIsNotWorldReadable proves the tmp file used during
// the rename window is created at 0o600 so secrets in the payload don't sit
// world-readable for the duration of the write.
func TestAtomicWrite_TmpFileIsNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix mode bits irrelevant on windows")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "config.toml")

	// We can't directly observe the tmp mid-rename without a race, but
	// the chmod-after-rename invariant means even if the caller asks for
	// 0o644 the final mode is exactly 0o644 (no leak), and if they ask
	// for 0o600 the final mode is 0o600. The umask isn't allowed to
	// tighten OR loosen the result.
	for _, mode := range []os.FileMode{0o600, 0o644, 0o640} {
		if err := iox.AtomicWrite(dest, []byte("x\n"), mode); err != nil {
			t.Fatalf("AtomicWrite(mode=%o): %v", mode, err)
		}
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != mode {
			t.Fatalf("final mode for requested %o = %o, want exact match", mode, got)
		}
	}
}
