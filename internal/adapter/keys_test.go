package adapter_test

import (
	"encoding/json"
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

func TestParseHookTimeout(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		timeout, ok, structural := adapter.ParseHookTimeout(map[string]any{"command": "x"})
		if timeout != 0 || !ok || structural {
			t.Fatalf("missing timeout: %d ok=%v structural=%v", timeout, ok, structural)
		}
	})
	t.Run("int", func(t *testing.T) {
		timeout, ok, structural := adapter.ParseHookTimeout(map[string]any{"timeout": 30})
		if timeout != 30 || !ok || structural {
			t.Fatalf("int timeout: %d ok=%v structural=%v", timeout, ok, structural)
		}
	})
	t.Run("json.Number", func(t *testing.T) {
		timeout, ok, structural := adapter.ParseHookTimeout(map[string]any{"timeout": json.Number("12")})
		if timeout != 12 || !ok || structural {
			t.Fatalf("json.Number timeout: %d ok=%v structural=%v", timeout, ok, structural)
		}
	})
	t.Run("negative", func(t *testing.T) {
		timeout, ok, structural := adapter.ParseHookTimeout(map[string]any{"timeout": -1})
		if timeout != 0 || ok || !structural {
			t.Fatalf("negative timeout: %d ok=%v structural=%v", timeout, ok, structural)
		}
	})
	t.Run("string", func(t *testing.T) {
		timeout, ok, structural := adapter.ParseHookTimeout(map[string]any{"timeout": "30"})
		if timeout != 0 || ok || !structural {
			t.Fatalf("string timeout: %d ok=%v structural=%v", timeout, ok, structural)
		}
	})
}

func TestSetHookTimeout(t *testing.T) {
	h := map[string]any{"command": "x"}
	adapter.SetHookTimeout(h, 0)
	if _, ok := h["timeout"]; ok {
		t.Fatal("zero timeout must omit the field")
	}
	adapter.SetHookTimeout(h, 8)
	if h["timeout"] != 8 {
		t.Fatalf("timeout not written: %#v", h)
	}
}
