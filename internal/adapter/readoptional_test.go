package adapter_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestReadFileOptional pins the (data,true,nil) / (nil,false,nil) / (nil,false,err)
// contract that every adapter ingest depends on to tell "component absent" from
// "component present but unreadable". Anchored to real on-disk files (a
// memmap/model round-trip cannot produce a real read error).
func TestReadFileOptional(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()

	t.Run("absent returns (nil,false,nil)", func(t *testing.T) {
		data, present, err := adapter.ReadFileOptional(filepath.Join(tmp, "does-not-exist"))
		if err != nil || present || data != nil {
			t.Fatalf("absent: got data=%v present=%v err=%v; want nil,false,nil", data, present, err)
		}
	})

	t.Run("present+readable returns (data,true,nil)", func(t *testing.T) {
		p := filepath.Join(tmp, "file.txt")
		if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
		data, present, err := adapter.ReadFileOptional(p)
		if err != nil || !present || string(data) != "hello" {
			t.Fatalf("present: got data=%q present=%v err=%v; want \"hello\",true,nil", data, present, err)
		}
	})

	t.Run("present+unreadable (dir at path) returns (nil,false,err)", func(t *testing.T) {
		// A directory where a file is expected → os.ReadFile fails with a
		// non-os.IsNotExist error (EISDIR). Deterministic and root-proof.
		p := filepath.Join(tmp, "isdir")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		data, present, err := adapter.ReadFileOptional(p)
		if err == nil {
			t.Fatalf("expected a non-nil error for a directory-at-file-path, got present=%v data=%v", present, data)
		}
		if present || data != nil {
			t.Fatalf("on error want (nil,false,err), got data=%v present=%v", data, present)
		}
		if os.IsNotExist(err) {
			t.Fatalf("a present-but-unreadable file must NOT report as IsNotExist: %v", err)
		}
	})
}

// TestReadDirOptional mirrors the file contract for directory listings: absent →
// silent skip; present+readable → entries; a regular file sitting where a dir is
// expected → a loud non-IsNotExist error (ENOTDIR).
func TestReadDirOptional(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()

	t.Run("absent returns (nil,false,nil)", func(t *testing.T) {
		entries, present, err := adapter.ReadDirOptional(filepath.Join(tmp, "no-such-dir"))
		if err != nil || present || entries != nil {
			t.Fatalf("absent: got entries=%v present=%v err=%v; want nil,false,nil", entries, present, err)
		}
	})

	t.Run("present+readable returns entries", func(t *testing.T) {
		d := filepath.Join(tmp, "realdir")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "a.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		entries, present, err := adapter.ReadDirOptional(d)
		if err != nil || !present || len(entries) != 1 {
			t.Fatalf("present: got entries=%d present=%v err=%v; want 1,true,nil", len(entries), present, err)
		}
	})

	t.Run("file where a dir is expected returns (nil,false,err)", func(t *testing.T) {
		p := filepath.Join(tmp, "notadir")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		entries, present, err := adapter.ReadDirOptional(p)
		if err == nil {
			t.Fatalf("expected a non-nil error listing a regular file as a dir, got present=%v", present)
		}
		if present || entries != nil {
			t.Fatalf("on error want (nil,false,err), got entries=%v present=%v", entries, present)
		}
		if os.IsNotExist(err) {
			t.Fatalf("a present-but-unreadable dir must NOT report as IsNotExist: %v", err)
		}
	})
}
