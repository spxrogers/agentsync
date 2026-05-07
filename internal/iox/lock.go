package iox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// Lock represents an acquired exclusive file lock. Release() drops it.
type Lock struct {
	fl *flock.Flock
}

// Release drops the lock. Idempotent.
func (l *Lock) Release() error {
	if l == nil || l.fl == nil {
		return nil
	}
	return l.fl.Unlock()
}

// AcquireLock takes an exclusive lock on path, blocking forever until it
// succeeds. The parent directory is created if missing.
func AcquireLock(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir parent of lock %s: %w", path, err)
	}
	fl := flock.New(path)
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{fl: fl}, nil
}

// AcquireLockTimeout takes an exclusive lock on path, returning an error if
// the lock cannot be acquired within timeout.
func AcquireLockTimeout(path string, timeout time.Duration) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir parent of lock %s: %w", path, err)
	}
	fl := flock.New(path)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	locked, err := fl.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	if !locked {
		return nil, fmt.Errorf("lock %s busy after %s", path, timeout)
	}
	return &Lock{fl: fl}, nil
}
