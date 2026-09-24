package adapter

import "github.com/spxrogers/agentsync/internal/source"

// Hook-handler field helpers shared by the hook-ingesting adapters (claude,
// codex, cursor, gemini, grok). They live here rather than in keys.go, which is
// about quoting and diffing unmodeled KEY NAMES, because these read and write a
// specific field's VALUE.
//
// The unit is the whole point of the split below. Claude Code, Codex, Cursor
// and Grok Build all document `timeout` in SECONDS; Gemini CLI's hooks
// reference documents "Execution timeout in milliseconds (default: 60000)".
// The canonical Hook.Timeout is seconds, so Gemini — and only Gemini — converts
// on both legs. Naming the unit in the function rather than hiding it in an
// argument keeps every call site self-evidently right or wrong at a glance.

// ParseHookTimeout reads a native handler's "timeout" expressed in SECONDS and
// returns it as a canonical Hook.Timeout. An absent field is HookTimeoutOK with
// value 0 (no timeout), which is the overwhelmingly common case.
func ParseHookTimeout(h map[string]any) (int, source.HookTimeoutParse) {
	return parseHookTimeoutUnits(h, 1)
}

// ParseHookTimeoutMillis reads a native handler's "timeout" expressed in
// MILLISECONDS (Gemini CLI) and converts it to canonical whole SECONDS. A
// native value that is not a whole number of seconds — Gemini's own 1500, say —
// is refused as unrepresentable rather than rounded, so agentsync never
// silently changes how long a user's hook is allowed to run.
func ParseHookTimeoutMillis(h map[string]any) (int, source.HookTimeoutParse) {
	return parseHookTimeoutUnits(h, 1000)
}

func parseHookTimeoutUnits(h map[string]any, perSecond int64) (int, source.HookTimeoutParse) {
	raw, present := h["timeout"]
	if !present {
		return 0, source.HookTimeoutOK
	}
	n, res := source.HookTimeoutNumber(raw)
	if res != source.HookTimeoutOK {
		return 0, res
	}
	// An explicit native 0 is a value the model cannot tell from "absent"; a
	// value that does not divide into whole seconds would have to be rounded.
	// Both are unrepresentable rather than malformed — the native shape is
	// perfectly valid, agentsync just cannot carry it. See
	// source.HookTimeoutUnrepresentable.
	if n == 0 || n%perSecond != 0 {
		return 0, source.HookTimeoutUnrepresentable
	}
	return int(n / perSecond), source.HookTimeoutOK
}

// SetHookTimeout writes a canonical timeout onto a native handler object in
// SECONDS, omitting the key entirely at 0 so a canonical source that carries no
// timeout renders byte-identically to one written before the field existed.
func SetHookTimeout(handler map[string]any, seconds int) {
	if seconds > 0 {
		handler["timeout"] = seconds
	}
}

// SetHookTimeoutMillis writes a canonical timeout onto a native handler object
// in MILLISECONDS (Gemini CLI). int64 arithmetic keeps the multiply honest on a
// 32-bit build; the loader caps canonical timeouts at source.MaxHookTimeout, so
// the product always fits.
func SetHookTimeoutMillis(handler map[string]any, seconds int) {
	if seconds > 0 {
		handler["timeout"] = int64(seconds) * 1000
	}
}
