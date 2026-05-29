// Package adaptertest holds test helpers shared by the adapter packages.
//
// It exists for one purpose today: centralise the os.Stderr-capture pipe
// pattern the per-adapter SetStderr-nil-resets tests use. Round-3 of the
// PR-#50 review surfaced that the pattern had been copy-pasted to three
// adapter test packages (claude / opencode / codex) and that the
// deferred-cleanup fix had to be applied identically to all three — the
// kind of triplication that silently invites drift the next time
// something subtle changes. Sharing the helper as a normal (non-_test)
// package follows the stdlib `httptest` precedent: ordinary code that
// happens to take a `*testing.T`.
//
// This package MUST NOT depend on any concrete adapter (claude /
// opencode / codex) — that would invert the layering, since each
// adapter package already imports adapter, and the test helper is
// shared across them.
package adaptertest

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// CaptureOsStderr swaps os.Stderr for a pipe, runs fn, then restores
// the original os.Stderr and returns whatever fn wrote to the pipe.
// Cleanup is deferred BEFORE invoking fn so a t.Fatalf / panic (both
// unwind via runtime.Goexit and through defers) does not leak the
// read goroutine or leave os.Stderr swapped for the rest of the
// process.
//
// Use it to verify an adapter's SetStderr(nil) actually routes back
// to the os.Stderr default — a faulty implementation routing to
// io.Discard would otherwise pass an "is the previous buffer empty?"
// assertion while silently dropping every future warning.
//
// Not safe for concurrent use within a package: it swaps a
// process-global. Callers MUST NOT t.Parallel a test that calls this.
func CaptureOsStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w

	// Deferred cleanup for the t.Fatalf / panic path: runtime.Goexit
	// unwinds through defers but skips bare statements AFTER fn(), so a
	// failed assertion inside fn would otherwise (a) leak the read
	// goroutine blocked on the open pipe and (b) leave os.Stderr
	// pointing at our closed write end, breaking later tests in the
	// same package. Double-Close on the happy path is harmless —
	// *os.File.Close returns an error on second call which we ignore.
	defer func() { os.Stderr = orig }()
	defer func() { _ = w.Close() }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	// Explicit close on the happy path is required: the deferred close
	// above runs only on function return, but `return <-done` blocks
	// FIRST waiting for the goroutine's send, which itself waits for
	// io.Copy to see EOF. Without this explicit close, the receive
	// would deadlock against the still-open write end.
	_ = w.Close()
	return <-done
}

// SwapOnFirstWriteBuffer is a bytes.Buffer that, on its first Write
// call, invokes OnFirstWrite (once) before writing. Used by per-
// adapter snapshot tests to swap the adapter's Stderr to a sibling
// buffer mid-Ingest. If the adapter snapshots `warn := a.stderr()`
// at Ingest entry (the documented adapter.WarnEmitter contract),
// subsequent warnings still land in this buffer; if the adapter
// instead re-reads `a.stderr()` per warning, they land in the
// sibling and the snapshot assertion catches it.
//
// Zero-value ready; set OnFirstWrite before passing to the adapter.
type SwapOnFirstWriteBuffer struct {
	bytes.Buffer
	OnFirstWrite func()
	fired        bool
}

// Write implements io.Writer.
func (b *SwapOnFirstWriteBuffer) Write(p []byte) (int, error) {
	if !b.fired {
		b.fired = true
		if b.OnFirstWrite != nil {
			b.OnFirstWrite()
		}
	}
	return b.Buffer.Write(p)
}

// Fired reports whether OnFirstWrite has been invoked. Tests use it
// to fail-loudly when the fixture didn't actually trigger any write
// (which would let the snapshot assertion pass vacuously).
func (b *SwapOnFirstWriteBuffer) Fired() bool { return b.fired }
