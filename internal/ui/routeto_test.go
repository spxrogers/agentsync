package ui

import (
	"bytes"
	"io"
	"testing"
)

// fakeSetter records the most recent SetStderr argument so the routing
// primitive can be exercised in isolation, without standing up an adapter,
// a printer, or the import command.
type fakeSetter struct {
	got     io.Writer
	calls   int
	lastNil bool
}

func (f *fakeSetter) SetStderr(w io.Writer) {
	f.got = w
	f.calls++
	f.lastNil = (w == nil)
}

// nonSetter has no SetStderr method; RouteTo must silently no-op on it
// (the "adapter doesn't implement WarnEmitter" path).
type nonSetter struct{}

// TestWarnWriter_RouteTo pins the routing primitive directly so the
// contract is not piggy-backed on the import command's full setup. If
// RouteTo ever drops its type-assert (or its structural interface drifts
// from adapter.WarnEmitter in signature), this test fails immediately —
// independent of any --color flag, lenient-YAML fixture, or printer state.
// The restore-closure shape is also tested here: every RouteTo call
// returns a callable closure, even on the no-op paths, so callers can
// safely `defer warnW.RouteTo(a)()` without nil-checking.
func TestWarnWriter_RouteTo(t *testing.T) {
	p := New(&bytes.Buffer{}, &bytes.Buffer{}, ColorNever)
	w := NewWarnWriter(&bytes.Buffer{}, p)

	t.Run("RouteTo passes the writer to a SetStderr implementor", func(t *testing.T) {
		var s fakeSetter
		restore := w.RouteTo(&s)
		if s.calls != 1 {
			t.Fatalf("RouteTo: SetStderr should be called exactly once; got %d", s.calls)
		}
		if s.got != w {
			t.Fatalf("RouteTo: SetStderr should receive the *WarnWriter; got %T", s.got)
		}
		if restore == nil {
			t.Fatal("RouteTo must always return a non-nil restore closure")
		}
		restore()
		if s.calls != 2 || !s.lastNil {
			t.Fatalf("restore must call SetStderr(nil); got calls=%d lastNil=%v", s.calls, s.lastNil)
		}
	})

	t.Run("restore is idempotent enough to defer safely", func(t *testing.T) {
		var s fakeSetter
		restore := w.RouteTo(&s)
		restore()
		restore() // second call sends another SetStderr(nil); same end state.
		if s.calls != 3 || !s.lastNil {
			t.Fatalf("repeated restore should keep the setter at nil; got calls=%d lastNil=%v",
				s.calls, s.lastNil)
		}
	})

	t.Run("RouteTo on a non-implementor is a silent no-op with a callable restore", func(t *testing.T) {
		// Restore is a no-op closure for non-implementors; callers can
		// always `defer warnW.RouteTo(a)()` without checking.
		restore := w.RouteTo(&nonSetter{})
		if restore == nil {
			t.Fatal("restore closure must be non-nil even on the no-op path")
		}
		restore() // must not panic.
	})

	t.Run("RouteTo on untyped nil is a silent no-op", func(t *testing.T) {
		// any(nil) carries no concrete type; the type-assert misses.
		// Important because adapter implementations may legitimately be
		// nil during error paths (e.g. reg.Lookup of an unregistered
		// agent — though the actual caller short-circuits earlier).
		restore := w.RouteTo(nil)
		if restore == nil {
			t.Fatal("restore closure must be non-nil even for nil arg")
		}
		restore()
	})

	t.Run("RouteTo on typed-nil pointer is a silent no-op (no panic)", func(t *testing.T) {
		// var a *fakeSetter = nil; w.RouteTo(a) — the interface value
		// carries the method set of *fakeSetter, so the type assertion
		// SUCCEEDS, but calling SetStderr on a nil pointer would
		// dereference and panic. RouteTo's reflect guard catches this.
		// Without the guard, this test panics.
		var nilSetter *fakeSetter
		restore := w.RouteTo(nilSetter)
		if restore == nil {
			t.Fatal("restore closure must be non-nil even for typed-nil arg")
		}
		restore() // must not panic.
	})
}
