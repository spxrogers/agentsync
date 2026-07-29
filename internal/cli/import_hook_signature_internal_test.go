package cli

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// TestHookSignature_Injective pins that the hook join key is injective for ANY
// byte content, including a part that itself contains the byte a separator-based
// encoding would use.
//
// Scope, stated honestly: the previous NUL-joined encoding was already injective
// for NUL-free inputs, so this is not closing a live hole. A forged collision
// needed BOTH handlers to carry an embedded NUL — the victim's own field
// included — which no real hand-written hook does. What was wrong was the code
// asserting an invariant ("NUL cannot appear in any of the parts") that the
// inputs do not guarantee: a plugin's hooks come from JSON, where NUL is a legal
// string escape, and Command is carried through verbatim. Length prefixes make
// the claim true by construction rather than by assumption, which is the point —
// the key decides whether a user's own handler is dropped from import.
func TestHookSignature_Injective(t *testing.T) {
	mk := func(event, matcher, typ, command string) source.Hook {
		return source.Hook{
			Event: untrusted.Wrap(event), Matcher: matcher, Type: typ, Command: command,
		}
	}
	nul := string(rune(0))

	// The case that actually distinguishes the two encodings. Under NUL-joining
	// both of these flatten to "E<NUL>M<NUL>X<NUL>T<NUL>C" — the same string from
	// two different handlers. Length prefixes keep them apart.
	a := mk("E", "M"+nul+"X", "T", "C")
	b := mk("E", "M", "X"+nul+"T", "C")
	if hookSignature(a) == hookSignature(b) {
		t.Errorf("handlers differing only in where an embedded NUL falls must not "+
			"share a signature; both = %q", hookSignature(a))
	}

	// It still has to actually JOIN: identical handlers must agree, or the filter
	// would never match a plugin's projected handler to the native ingest.
	victim := mk("PreToolUse", "Bash", "command", "mine")
	if hookSignature(victim) != hookSignature(mk("PreToolUse", "Bash", "command", "mine")) {
		t.Error("identical handlers must produce identical signatures")
	}

	// And genuinely different handlers stay different — the ordinary case.
	if hookSignature(victim) == hookSignature(mk("PreToolUse", "Bash", "command", "theirs")) {
		t.Error("handlers with different commands must not share a signature")
	}
	if hookSignature(victim) == hookSignature(mk("PostToolUse", "Bash", "command", "mine")) {
		t.Error("handlers on different events must not share a signature")
	}
}
