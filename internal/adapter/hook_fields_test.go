package adapter_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// hookTimeoutCase drives both ParseHookTimeout and ParseHookTimeoutMillis.
// absent distinguishes "no timeout key" from a key whose value is nil, which
// are different inputs with different outcomes (OK vs malformed).
type hookTimeoutCase struct {
	name   string
	raw    any
	absent bool
	want   int
	res    source.HookTimeoutParse
}

func runHookTimeoutCases(t *testing.T, cases []hookTimeoutCase, parse func(map[string]any) (int, source.HookTimeoutParse)) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := map[string]any{"command": "echo hi"}
			if !tc.absent {
				h["timeout"] = tc.raw
			}
			got, res := parse(h)
			if got != tc.want || res != tc.res {
				t.Fatalf("parse(%#v) = (%d, %v); want (%d, %v)", tc.raw, got, res, tc.want, tc.res)
			}
		})
	}
}

// TestParseHookTimeout pins the seconds reading used by claude, codex, cursor
// and grok. The classification matters as much as the value: Unrepresentable is
// a SEMANTIC refusal that still retires canonical config, Malformed is a
// STRUCTURAL one that must not (#124), so a row moving between the two is a
// behavior change in import's stale-hook retirement.
func TestParseHookTimeout(t *testing.T) {
	runHookTimeoutCases(t, []hookTimeoutCase{
		{name: "absent is no timeout", absent: true, want: 0, res: source.HookTimeoutOK},

		// Representable values, across every numeric shape a decoder here emits.
		{name: "int", raw: 30, want: 30, res: source.HookTimeoutOK},
		{name: "int64", raw: int64(45), want: 45, res: source.HookTimeoutOK},
		{name: "int32", raw: int32(7), want: 7, res: source.HookTimeoutOK},
		{name: "float64 whole", raw: float64(30), want: 30, res: source.HookTimeoutOK},
		{name: "float32 whole", raw: float32(9), want: 9, res: source.HookTimeoutOK},
		{name: "json.Number integer", raw: json.Number("12"), want: 12, res: source.HookTimeoutOK},
		{name: "json.Number whole float", raw: json.Number("25.0"), want: 25, res: source.HookTimeoutOK},
		{name: "json.Number exponent", raw: json.Number("1e2"), want: 100, res: source.HookTimeoutOK},
		{name: "max", raw: int(source.MaxHookTimeout), want: source.MaxHookTimeout, res: source.HookTimeoutOK},

		// Numbers the canonical int cannot carry: semantic, never structural.
		{name: "explicit zero", raw: 0, want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "json.Number zero", raw: json.Number("0"), want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "negative int", raw: -1, want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "negative float64", raw: float64(-3), want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "negative json.Number", raw: json.Number("-1"), want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "fractional float64", raw: 30.5, want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "fractional json.Number", raw: json.Number("1.5"), want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "beyond int64", raw: json.Number("99999999999999999999"), want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "beyond max int64", raw: int64(source.MaxHookTimeout) + 1, want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "beyond max float", raw: float64(source.MaxHookTimeout) * 10, want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "NaN", raw: math.NaN(), want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "infinity", raw: math.Inf(1), want: 0, res: source.HookTimeoutUnrepresentable},

		// Not a number at all: structural, so retirement never fires on a typo.
		{name: "string", raw: "30", want: 0, res: source.HookTimeoutMalformed},
		{name: "empty string", raw: "", want: 0, res: source.HookTimeoutMalformed},
		{name: "bool", raw: true, want: 0, res: source.HookTimeoutMalformed},
		{name: "null", raw: nil, want: 0, res: source.HookTimeoutMalformed},
		{name: "object", raw: map[string]any{"seconds": 30}, want: 0, res: source.HookTimeoutMalformed},
		{name: "array", raw: []any{30}, want: 0, res: source.HookTimeoutMalformed},
		{name: "unparseable json.Number", raw: json.Number("not-a-number"), want: 0, res: source.HookTimeoutMalformed},
	}, adapter.ParseHookTimeout)
}

// TestParseHookTimeoutMillis pins the Gemini leg. Gemini's hooks reference
// documents `timeout` as "Execution timeout in milliseconds (default: 60000)",
// so the same native number means something a thousand times different there
// than it does in every other harness.
func TestParseHookTimeoutMillis(t *testing.T) {
	runHookTimeoutCases(t, []hookTimeoutCase{
		{name: "absent is no timeout", absent: true, want: 0, res: source.HookTimeoutOK},
		{name: "whole seconds", raw: 45000, want: 45, res: source.HookTimeoutOK},
		{name: "gemini default 60000ms is 60s", raw: 60000, want: 60, res: source.HookTimeoutOK},
		{name: "json.Number", raw: json.Number("30000"), want: 30, res: source.HookTimeoutOK},
		{name: "float64 whole", raw: float64(5000), want: 5, res: source.HookTimeoutOK},

		// Sub-second values are refused, never rounded: agentsync must not
		// silently change how long a hook may run.
		{name: "sub-second is refused not rounded", raw: 1500, want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "under a second", raw: 30, want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "explicit zero", raw: 0, want: 0, res: source.HookTimeoutUnrepresentable},
		{name: "negative", raw: -1000, want: 0, res: source.HookTimeoutUnrepresentable},

		{name: "string", raw: "45000", want: 0, res: source.HookTimeoutMalformed},
		{name: "null", raw: nil, want: 0, res: source.HookTimeoutMalformed},
	}, adapter.ParseHookTimeoutMillis)
}

func TestSetHookTimeout(t *testing.T) {
	t.Run("zero omits the key", func(t *testing.T) {
		h := map[string]any{"command": "x"}
		adapter.SetHookTimeout(h, 0)
		if _, ok := h["timeout"]; ok {
			t.Fatalf("zero timeout must omit the field, got %#v", h)
		}
		adapter.SetHookTimeoutMillis(h, 0)
		if _, ok := h["timeout"]; ok {
			t.Fatalf("zero millis timeout must omit the field, got %#v", h)
		}
	})
	t.Run("seconds", func(t *testing.T) {
		h := map[string]any{"command": "x"}
		adapter.SetHookTimeout(h, 8)
		if h["timeout"] != 8 {
			t.Fatalf("timeout not written as seconds: %#v", h)
		}
	})
	t.Run("millis multiplies", func(t *testing.T) {
		h := map[string]any{"command": "x"}
		adapter.SetHookTimeoutMillis(h, 45)
		if h["timeout"] != int64(45000) {
			t.Fatalf("timeout not written as milliseconds: %#v", h)
		}
	})
}

// TestHookTimeoutUnitsRoundTrip is the guard that would have caught the Gemini
// unit bug: emit through each Set*, read back through its paired Parse*, and
// require the canonical seconds to survive. A seconds renderer paired with a
// millisecond reader (or the reverse) fails here even though a same-adapter
// round trip — which is what the original Gemini test did — stays green,
// because the error cancels itself out when both legs share the mistake.
func TestHookTimeoutUnitsRoundTrip(t *testing.T) {
	for _, seconds := range []int{1, 5, 30, 60, 3600} {
		h := map[string]any{"command": "x"}
		adapter.SetHookTimeout(h, seconds)
		if got, res := adapter.ParseHookTimeout(h); got != seconds || res != source.HookTimeoutOK {
			t.Errorf("seconds round trip %d: got (%d, %v)", seconds, got, res)
		}
		// The seconds wire value must NOT read back as the same number of
		// seconds through the millisecond reader.
		if got, res := adapter.ParseHookTimeoutMillis(h); res == source.HookTimeoutOK && got == seconds {
			t.Errorf("seconds %d read as %d through the millisecond parser; the units are not interchangeable", seconds, got)
		}

		m := map[string]any{"command": "x"}
		adapter.SetHookTimeoutMillis(m, seconds)
		if got, res := adapter.ParseHookTimeoutMillis(m); got != seconds || res != source.HookTimeoutOK {
			t.Errorf("millis round trip %d: got (%d, %v)", seconds, got, res)
		}
		if m["timeout"] != int64(seconds)*1000 {
			t.Errorf("millis wire value for %ds = %#v; want %d", seconds, m["timeout"], seconds*1000)
		}
	}
}
