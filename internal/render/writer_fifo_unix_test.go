//go:build unix

package render_test

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
)

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
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	op := adapter.FileOp{Action: "delete", Path: fifo, SourceID: "subagents/pipe.md"}

	done := make(chan bool, 1) // buffered so the goroutine cannot leak on timeout
	go func() { done <- render.OrphanDeleteWillProceed(op) }()
	select {
	case got := <-done:
		if got {
			t.Error("a FIFO cannot be read or preserved; it must not be reported as reclaimable")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OrphanDeleteWillProceed BLOCKED on a FIFO — `apply --dry-run` is advertised " +
			"as read-only and would hang forever")
	}
}
