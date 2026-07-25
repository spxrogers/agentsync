package adapter_test

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// TestQuotedKeys_EscapesControlBytes backs the security claim QuotedKeys' doc
// makes ("a key containing a newline cannot forge a second warning line") with
// a direct pin — the transitive coverage through the adapter guard tables only
// ever passes benign keys, which proves %q is applied, not that it escapes
// (round-4 review; the repo rule: doc claims are not self-certifying).
func TestQuotedKeys_EscapesControlBytes(t *testing.T) {
	got := adapter.QuotedKeys([]string{"a\nwarning: forged", "b\x1b[31m"})
	if strings.ContainsAny(got, "\n\x1b") {
		t.Fatalf("control bytes must be escaped, got %q", got)
	}
	if got != `"a\nwarning: forged", "b\x1b[31m"` {
		t.Fatalf("unexpected rendering: %s", got)
	}
	// Benign keys stay readable — quoted, comma-separated, order preserved.
	if got := adapter.QuotedKeys([]string{"timeout", "failClosed"}); got != `"timeout", "failClosed"` {
		t.Fatalf("benign keys mis-rendered: %s", got)
	}
}

// TestUnmodeledKeys pins the helper's full contract: filter to keys absent
// from modeled, sorted output, and nil-safety on both arguments.
func TestUnmodeledKeys(t *testing.T) {
	m := map[string]any{"zeta": 1, "command": "x", "alpha": true}
	modeled := map[string]bool{"command": true}
	got := adapter.UnmodeledKeys(m, modeled)
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Fatalf("want sorted [alpha zeta], got %v", got)
	}
	if got := adapter.UnmodeledKeys(nil, modeled); len(got) != 0 {
		t.Fatalf("nil map must yield no keys, got %v", got)
	}
	if got := adapter.UnmodeledKeys(m, nil); len(got) != 3 {
		t.Fatalf("nil modeled set must treat every key as unmodeled, got %v", got)
	}
}
