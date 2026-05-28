package ui

import (
	"bytes"
	"testing"
)

// A *bytes.Buffer is never a terminal, so the spinner must be a complete
// no-op: no animation frames, no static fallback line, no escape codes. This
// is what keeps captured-output tests of `update` / `marketplace add`
// byte-stable when we wrap their network calls.
func TestSpinnerNoOpOffTerminal(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, &buf, ColorAuto)

	stop := p.Spin("fetching marketplace x")
	stop()

	if buf.Len() != 0 {
		t.Fatalf("spinner on a non-terminal writer must produce no output; got %q", buf.String())
	}
}

// Stop must be safe to call twice (typical pattern is a manual stop followed
// by a deferred stop on the error path) and Start must be safe to skip.
func TestSpinnerIdempotent(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, &buf, ColorAuto)

	s := p.Spinner("x")
	s.Stop() // before Start — must not block or panic
	s.Stop() // again
	s.Start()
	s.Start() // again
	s.Stop()
	s.Stop()

	if buf.Len() != 0 {
		t.Fatalf("expected no output; got %q", buf.String())
	}
}
