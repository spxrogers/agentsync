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

// nonSetter has no SetStderr method; RouteTo/Unroute must silently no-op on
// it (the "adapter doesn't implement WarnSink" path).
type nonSetter struct{}

// TestWarnWriter_RouteTo_AndUnroute pins the routing primitive directly so
// the contract is not piggy-backed on the import command's full setup. If
// RouteTo ever drops its type-assert (or its structural interface drifts
// from adapter.WarnSink in signature), this test fails immediately —
// independent of any --color flag, lenient-YAML fixture, or printer state.
func TestWarnWriter_RouteTo_AndUnroute(t *testing.T) {
	p := New(&bytes.Buffer{}, &bytes.Buffer{}, ColorNever)
	w := NewWarnWriter(&bytes.Buffer{}, p)

	t.Run("RouteTo passes the writer to a SetStderr implementor", func(t *testing.T) {
		var s fakeSetter
		w.RouteTo(&s)
		if s.calls != 1 {
			t.Fatalf("RouteTo: SetStderr should be called exactly once; got %d", s.calls)
		}
		if s.got != w {
			t.Fatalf("RouteTo: SetStderr should receive the *WarnWriter; got %T", s.got)
		}
	})

	t.Run("Unroute passes nil to reset to default", func(t *testing.T) {
		var s fakeSetter
		w.RouteTo(&s)
		w.Unroute(&s)
		if s.calls != 2 {
			t.Fatalf("Unroute should issue a second SetStderr call; got total %d", s.calls)
		}
		if !s.lastNil {
			t.Fatalf("Unroute must call SetStderr(nil); got non-nil writer %T", s.got)
		}
	})

	t.Run("RouteTo on a non-implementor is a silent no-op", func(t *testing.T) {
		// If the type assertion ever changes (e.g. someone replaces the
		// structural setter with a named adapter.WarnSink and forgets to
		// import-cycle-break), this case will fail because non-implementors
		// would no longer compile, or RouteTo would panic on the assertion.
		// Today: pure no-op.
		w.RouteTo(&nonSetter{})
		w.Unroute(&nonSetter{})
		// No panic, no observable effect — the test passing IS the assertion.
	})

	t.Run("RouteTo with nil receiver-arg is a silent no-op", func(t *testing.T) {
		// any(nil) carries no concrete type; the type-assert misses. Important
		// because Adapter implementations may legitimately be nil during error
		// paths (e.g. reg.Lookup of an unregistered agent).
		w.RouteTo(nil)
		w.Unroute(nil)
	})
}
