package adapter

import (
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
