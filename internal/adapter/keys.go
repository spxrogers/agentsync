package adapter

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// UnmodeledKeys returns the sorted keys of m that are not in modeled — the
// fields of a decoded native hook def/handler the canonical model cannot
// represent. Shared by the hook-ingesting adapters' refusal guards (claude,
// gemini, cursor, codex), which are otherwise deliberately per-package (their
// nesting/ordering semantics differ); this pair carries zero per-adapter
// divergence. Pair with QuotedKeys when the result reaches a warning line.
func UnmodeledKeys(m map[string]any, modeled map[string]bool) []string {
	var out []string
	for k := range m {
		if !modeled[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// QuotedKeys renders untrusted native key names for a warning line: each key
// %q-quoted (control bytes escaped — a key containing a newline cannot forge a
// second warning line), comma-separated. This is a log-forging defense; it
// lives here, in one place, so no adapter's warning path can drift back to
// raw interpolation.
func QuotedKeys(keys []string) string {
	qs := make([]string, len(keys))
	for i, k := range keys {
		qs[i] = strconv.Quote(k)
	}
	return strings.Join(qs, ", ")
}

// ParseHookTimeout reads a native handler's timeout (seconds). Missing is
// ok with value 0. A present non-integer is malformed (structural).
func ParseHookTimeout(h map[string]any) (timeout int, ok bool, structural bool) {
	raw, present := h["timeout"]
	if !present {
		return 0, true, false
	}
	switch n := raw.(type) {
	case int:
		if n < 0 {
			return 0, false, true
		}
		return n, true, false
	case int64:
		if n < 0 {
			return 0, false, true
		}
		return int(n), true, false
	case float64:
		if n < 0 || n != float64(int(n)) {
			return 0, false, true
		}
		return int(n), true, false
	case json.Number:
		i, err := n.Int64()
		if err != nil || i < 0 {
			return 0, false, true
		}
		return int(i), true, false
	default:
		return 0, false, true
	}
}

// SetHookTimeout writes timeout onto a native handler object when > 0.
func SetHookTimeout(handler map[string]any, timeout int) {
	if timeout > 0 {
		handler["timeout"] = timeout
	}
}
