//go:build unix

package cli

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestHashFile_FIFODoesNotBlock covers status's destination-read guard. `status`
// is advertised as read-only, so a FIFO someone left at a managed destination
// path must not wedge it: os.ReadFile does not fail on a FIFO, it waits forever
// for a writer that never comes.
//
// It runs under a timeout because the regression is a HANG, not a wrong answer —
// a plain assertion would never report. The sentinel must also be distinct from
// the symlink one so a diagnostic never calls a FIFO a symlink.
//
// unix-only: syscall.Mkfifo is undefined on Windows.
func TestHashFile_FIFODoesNotBlock(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pipe.md")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}

	var got string
	done := make(chan struct{})
	go func() { defer close(done); got = hashFile(fifo) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hashFile BLOCKED on a FIFO — os.ReadFile does not fail on one, it waits " +
			"for a writer that never comes")
	}

	if got != "not-a-regular-file" {
		t.Errorf("a FIFO has no content hash worth computing; want the non-regular sentinel, got %q", got)
	}
	if got == "symlink-not-regular-file" {
		t.Error("a FIFO must not be reported as a symlink")
	}
}
