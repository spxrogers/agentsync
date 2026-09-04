//go:build unix

package cli

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/state"
)

// TestWalkPlanItems_FIFODestAnswersTheShapeSentinelAndDoesNotBlock is the
// unix-only N-12 subtest: a FIFO at a whole-file destination must not wedge the
// walk (os.ReadFile on a FIFO blocks in the open rather than failing), and the
// answer must be the SHAPE sentinel on the hash side and "" on the text side —
// asserted by value, so a skipped Mkfifo cannot quietly become a permanent hole
// that a "returned in time" check would hide.
func TestWalkPlanItems_FIFODestAnswersTheShapeSentinelAndDoesNotBlock(t *testing.T) {
	h := t.TempDir()
	fifo := filepath.Join(h, "dest", "pipe.md")
	mustWrite(t, filepath.Join(h, "dest", ".keep"), "")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	plan := planFor(map[string][]adapter.FileOp{"claude": {fileOp(fifo, "SOURCE")}})

	var items []planItem
	done := make(chan struct{})
	go func() {
		defer close(done)
		items = walkUser(h, plan, state.New(), []string{"claude"}, func(w *planWalk) { w.withText = true })
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("walkPlanItems BLOCKED on a FIFO destination")
	}
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	it := items[0]
	if it.hdest != "not-a-regular-file" {
		t.Errorf("hash side must answer the shape sentinel, got %q", it.hdest)
	}
	if it.dstText != "" || it.srcText != "SOURCE" {
		t.Errorf("text side must be empty for a refused shape: src=%q dst=%q", it.srcText, it.dstText)
	}
	if it.destRegular {
		t.Errorf("a FIFO is not a regular file for the mode predicates")
	}
}
