//go:build unix

package render_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// withinTimeout runs fn and fails if it has not returned within 5s. Every guard
// in this file defends against a HANG rather than a wrong answer, so a plain
// assertion would never report — the test would just never finish.
func withinTimeout(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }() // buffered by close, cannot leak
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s BLOCKED on a FIFO — os.ReadFile does not fail on one, it waits "+
			"for a writer that never comes", what)
	}
}

// mkfifo creates a FIFO or skips the test where that is unsupported.
func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
}

// TestOrphanDeleteWillProceed_FIFO covers the non-regular short-circuit, which
// exists ONLY for shapes whose open BLOCKS rather than fails — a FIFO is the
// reachable one. A directory answers false via the read anyway, so without this
// case the whole branch could be deleted with the main table still green.
//
// It runs under a timeout because the regression is a HANG, not a wrong answer:
// a plain assertion would never report. `apply --dry-run` is advertised as
// read-only, so wedging it on a FIFO someone left at a destination path is the
// failure this prevents.
//
// unix-only: syscall.Mkfifo is undefined on Windows.
func TestOrphanDeleteWillProceed_FIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pipe.md")
	mkfifo(t, fifo)
	op := adapter.FileOp{Action: "delete", Path: fifo, SourceID: "subagents/pipe.md"}
	withinTimeout(t, "OrphanDeleteWillProceed", func() {
		if render.OrphanDeleteWillProceed(op) {
			t.Error("a FIFO cannot be read or preserved; it must not be reported as reclaimable")
		}
	})
}

// TestWriterDelete_FIFODoesNotBlock covers the guard that matters MOST, and the
// one a summary predicate cannot stand in for: OrphanDeleteWillProceed only
// decides a COUNT, while Writer.Delete is the call a real `apply` makes. Its
// pre-delete read is the one that would wedge the run.
//
// The FIFO must also survive: agentsync cannot read it, so it cannot rule out
// content worth preserving, so it must not remove it.
func TestWriterDelete_FIFODoesNotBlock(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(tmp, "pipe.md")
	mkfifo(t, fifo)

	w := render.NewWriter(state.New(), home, tmp, adapter.ScopeUser, "", "claude")
	op := adapter.FileOp{Action: "delete", Path: fifo, SourceID: "subagents/pipe.md"}
	withinTimeout(t, "Writer.Delete", func() {
		if err := w.Delete(op); err != nil {
			t.Errorf("a non-regular destination must be SKIPPED, not error the run: %v", err)
		}
	})
	if _, err := os.Lstat(fifo); err != nil {
		t.Error("agentsync cannot read a FIFO, so it cannot rule out content worth " +
			"preserving — it must not remove it")
	}
}

// TestBackupFile_FIFODoesNotBlock covers reconcile's interactive orphan delete,
// which backs up before removing. Erroring is the point: the caller removes only
// if the backup succeeded, so refusing to back up also refuses the removal.
func TestBackupFile_FIFODoesNotBlock(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(tmp, "pipe.md")
	mkfifo(t, fifo)

	withinTimeout(t, "render.BackupFile", func() {
		if _, err := render.BackupFile(home, fifo); err == nil {
			t.Error("a destination agentsync cannot preserve must not report a successful " +
				"backup — the caller would then delete it")
		}
	})
}
