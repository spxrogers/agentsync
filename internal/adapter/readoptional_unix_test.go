//go:build unix

package adapter_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/adapter"
)

func TestReadFileOptional_FIFODoesNotBlock(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	done := make(chan struct{})
	var data []byte
	var present bool
	var err error
	go func() {
		defer close(done)
		data, present, err = adapter.ReadFileOptional(fifo)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadFileOptional BLOCKED on a FIFO")
	}
	if err == nil || present || data != nil {
		t.Fatalf("FIFO: got data=%v present=%v err=%v; want nil,false,err", data, present, err)
	}
	if !errors.Is(err, adapter.ErrNotRegularFile) {
		t.Fatalf("FIFO: want ErrNotRegularFile, got %v", err)
	}
	if os.IsNotExist(err) {
		t.Fatalf("a present FIFO must not report as absent: %v", err)
	}
}
