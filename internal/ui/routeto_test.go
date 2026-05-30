package ui

import (
	"bytes"
	"io"
	"strings"
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

	t.Run("restore is safe to call twice", func(t *testing.T) {
		// Strictly not idempotent — each restore call DOES invoke
		// SetStderr(nil) — but the end state is unchanged, which is
		// what makes `defer warnW.RouteTo(a)()` safe even if the
		// caller ever explicitly restored earlier in the function.
		var s fakeSetter
		restore := w.RouteTo(&s)
		restore()
		restore()
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

// TestWarnWriter_Flush pins the partial-line drain: a Write without a
// terminating newline sits in the line-assembly buffer until either a
// later newline arrives or Flush surrenders it. importRun does
// `defer warnW.Flush()` so a partial line a panicking adapter left in
// the buffer still reaches the user's terminal. Without this test,
// Flush could silently become a no-op and every other test would still
// pass (all current emitters terminate lines with \n).
func TestWarnWriter_Flush(t *testing.T) {
	var dest bytes.Buffer
	p := New(&bytes.Buffer{}, &dest, ColorNever)
	w := NewWarnWriter(&dest, p)

	t.Run("partial 'warning: ' line drains and gets styled on Flush", func(t *testing.T) {
		dest.Reset()
		_, _ = w.Write([]byte("warning: partial line no newline"))
		// Before Flush: nothing yet emitted because the line is incomplete.
		if dest.Len() != 0 {
			t.Fatalf("partial line should sit in the buffer until Flush; got: %q", dest.String())
		}
		w.Flush()
		got := dest.String()
		// Color is off (ColorNever), so the styled prefix is the bare
		// glyph + "warning:" — emit's HasPrefix("warning: ") still
		// triggers the prefix rewrite even without a trailing newline.
		const wantPrefix = GlyphWarnEmoji + " warning:"
		if !strings.HasPrefix(got, wantPrefix) {
			t.Fatalf("Flush should style the partial warning prefix; got: %q", got)
		}
		if !strings.Contains(got, "partial line no newline") {
			t.Fatalf("Flush should drain the body too; got: %q", got)
		}
	})

	t.Run("partial non-warning line drains verbatim", func(t *testing.T) {
		dest.Reset()
		w2 := NewWarnWriter(&dest, p)
		_, _ = w2.Write([]byte("note: agentsync did things"))
		w2.Flush()
		if got := dest.String(); got != "note: agentsync did things" {
			t.Fatalf("non-warning partial line should drain verbatim; got: %q", got)
		}
	})

	t.Run("Flush on empty buffer is a no-op", func(t *testing.T) {
		dest.Reset()
		w3 := NewWarnWriter(&dest, p)
		w3.Flush()
		if dest.Len() != 0 {
			t.Fatalf("Flush on empty buffer should not write; got: %q", dest.String())
		}
	})
}
