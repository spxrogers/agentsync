package iox_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/iox"
)

func TestLock_AcquireRelease(t *testing.T) {
	p := filepath.Join(t.TempDir(), "test.lock")
	l, err := iox.AcquireLock(p)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestLock_SecondAcquireBlocks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "test.lock")

	l1, err := iox.AcquireLock(p)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	t.Cleanup(func() { _ = l1.Release() })

	var (
		wg     sync.WaitGroup
		result error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		l2, err := iox.AcquireLockTimeout(p, 100*time.Millisecond)
		if err == nil {
			_ = l2.Release()
		}
		result = err
	}()
	wg.Wait()
	if result == nil {
		t.Fatalf("second AcquireLockTimeout returned nil; expected lock-busy error")
	}
}
