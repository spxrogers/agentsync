package render_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

func TestWriterHostPermissionConvergence(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "note.md")
	content := []byte("unchanged\n")
	if err := os.WriteFile(dest, content, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dest, 0o666); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	op := adapter.FileOp{Action: adapter.ActionWrite, Path: dest, Content: content, Mode: 0o644, SourceID: "note.md"}
	for _, dry := range []bool{true, false} {
		w := render.NewWriter(state.New(), filepath.Join(root, ".agentsync"), root, adapter.ScopeUser, "", "codex")
		if dry {
			w = render.NewPreviewWriter(state.New(), filepath.Join(root, ".agentsync"), root, adapter.ScopeUser, "", "codex")
		}
		if err := w.Write(op, content); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" && !w.Unchanged()[dest] {
			t.Fatal("Windows equivalent permissions must be unchanged")
		}
	}
	after, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("mode convergence rewrote content")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0o644 {
		t.Fatalf("Unix mode not corrected: %o", after.Mode().Perm())
	}
	if err := os.Chmod(dest, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest, 0o644) })
	w := render.NewWriter(state.New(), filepath.Join(root, ".agentsync"), root, adapter.ScopeUser, "", "codex")
	if err := w.Write(op, content); err != nil {
		t.Fatal(err)
	}
	if w.Unchanged()[dest] {
		t.Fatal("read-only mismatch must be corrected")
	}
}
