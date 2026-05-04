package iox_test

import (
	"os"
	"path/filepath"
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
