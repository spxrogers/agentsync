package source

import (
	"encoding/json"
	"math"
)

// MaxHookTimeout is the largest canonical Hook.Timeout, in seconds. The
// ceiling is MaxInt32/1000 so Gemini's render (seconds × 1000 milliseconds)
// always fits in a 32-bit int and re-ingests as the same canonical value:
// every timeout an adapter can write is one it can read back. A larger cap
// lets apply emit a millisecond figure import then treats as unmodelable,
// which retires hooks/<event>.toml for every agent. ~24.8 days; nothing
// legitimate is lost.
const MaxHookTimeout = math.MaxInt32 / 1000

// HookTimeoutParse classifies a native "timeout" value against what
// Hook.Timeout is able to represent.
//
// The split is load-bearing, not cosmetic. Import's stale-hook retirement
// deletes canonical config for every SEMANTICALLY refused event, so a native
// typo must never reach that path (#124): a shape agentsync cannot parse warns
// and skips capture but leaves hooks/<event>.toml alone, while a well-formed
// value agentsync merely cannot model is refused like any other unmodeled
// field and does retire. Classifying an unmodelable NUMBER as malformed would
// reopen that hole from the other side — the event would stop retiring, and the
// next apply would rewrite the user's native array without their timeout.
type HookTimeoutParse int

const (
	// HookTimeoutOK is a value Hook.Timeout carries exactly.
	HookTimeoutOK HookTimeoutParse = iota

	// HookTimeoutUnrepresentable is a well-formed NUMBER the canonical model
	// cannot carry: negative, fractional, not a whole number of seconds after
	// a harness's unit conversion, beyond MaxHookTimeout seconds, or an
	// explicit zero. Zero is unrepresentable because Timeout == 0 is how the
	// model spells "no timeout key at all" — a native 0 captured as 0 would
	// re-render with the key gone, silently swapping the harness default in
	// for the value the user wrote. A SEMANTIC refusal: the native shape is
	// valid, so the event both stays uncaptured and still retires.
	HookTimeoutUnrepresentable

	// HookTimeoutMalformed is not a number at all — a string, bool, object,
	// array or null, i.e. a native typo. A STRUCTURAL refusal: warn, skip
	// capture, never retire.
	HookTimeoutMalformed
)

// Reason renders the clause an adapter's ingest warning uses to tell the user
// which of the two refusals they hit, so "fix the typo" and "agentsync cannot
// model this" do not read identically. Empty for HookTimeoutOK.
func (p HookTimeoutParse) Reason() string {
	switch p {
	case HookTimeoutUnrepresentable:
		return `a "timeout" agentsync cannot represent (want a positive whole number of seconds; a millisecond timeout, such as Gemini's, must be a multiple of 1000)`
	case HookTimeoutMalformed:
		return `a "timeout" that is not a number`
	default:
		return ""
	}
}

// Structural reports whether p is a malformed native shape rather than an
// unmodelable value — the flag each adapter's ingest feeds into its refusal
// bookkeeping.
func (p HookTimeoutParse) Structural() bool { return p == HookTimeoutMalformed }

// HookTimeoutNumber interprets one native JSON or TOML number as a
// non-negative whole count of time units that fits in int64. It does not
// apply MaxHookTimeout: that ceiling is in canonical seconds, and a
// millisecond field is a thousand times larger than the seconds it converts
// to. Callers convert units first, then reject a result above MaxHookTimeout
// (adapter.parseHookTimeoutUnits, pluginHookTimeout).
//
// Every numeric shape a decoder in this repo can produce is handled: int (TOML,
// and Go literals in tests), int64 (TOML integers), float64 (encoding/json
// without UseNumber, e.g. plugin hooks.json), and json.Number (the adapters,
// which decode with UseNumber). A whole-valued float such as 30.0 or 1e2 is
// accepted — it is the same number — while 1.5 is refused rather than rounded.
func HookTimeoutNumber(raw any) (int64, HookTimeoutParse) {
	switch n := raw.(type) {
	case int:
		return hookTimeoutFromInt64(int64(n))
	case int32:
		return hookTimeoutFromInt64(int64(n))
	case int64:
		return hookTimeoutFromInt64(n)
	case float32:
		return hookTimeoutFromFloat(float64(n))
	case float64:
		return hookTimeoutFromFloat(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return hookTimeoutFromInt64(i)
		}
		// Int64 also fails on "30.0" and on an integer too large for int64, so
		// fall through to the float reading rather than calling either one a
		// typo. json.Number("1e400") does not land on +Inf: Float64 returns
		// strconv.ErrRange first, and that is a malformed reading (the token
		// is not a finite number), not an unrepresentable one.
		f, err := n.Float64()
		if err != nil {
			return 0, HookTimeoutMalformed
		}
		return hookTimeoutFromFloat(f)
	default:
		return 0, HookTimeoutMalformed
	}
}

func hookTimeoutFromInt64(n int64) (int64, HookTimeoutParse) {
	if n < 0 {
		return 0, HookTimeoutUnrepresentable
	}
	return n, HookTimeoutOK
}

func hookTimeoutFromFloat(f float64) (int64, HookTimeoutParse) {
	// Range-check BEFORE the int64 conversion: converting a float outside
	// int64's range is undefined in Go and would produce a garbage timeout.
	// The canonical MaxHookTimeout ceiling is applied by the caller after
	// unit conversion, not here.
	// 0x1p63 is 2^63, the smallest float64 strictly above MaxInt64. Comparing
	// against float64(MaxInt64) is wrong: that conversion rounds up to 2^63,
	// so a value of 2^63 would pass and then overflow the int64 conversion.
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f >= 0x1p63 {
		return 0, HookTimeoutUnrepresentable
	}
	if f != math.Trunc(f) {
		return 0, HookTimeoutUnrepresentable
	}
	return int64(f), HookTimeoutOK
}

// ValidHookTimeout reports whether a canonical Hook.Timeout value is one the
// renderers can emit. The loader rejects anything else at load time so a
// hand-edited hooks/<event>.toml fails loudly instead of having its timeout
// quietly dropped by SetHookTimeout's `> 0` guard.
func ValidHookTimeout(seconds int) bool {
	return seconds >= 0 && int64(seconds) <= MaxHookTimeout
}
