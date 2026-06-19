// Package untrusted carries strings that originate from OUTSIDE agentsync's
// trust boundary — fetched marketplace/plugin metadata (ids, versions,
// marketplace names) and native-config-derived plugin names — and that must be
// sanitized before they reach a terminal.
//
// # The Text type and why it is the enforcement
//
// A bare `string` field gives a future print site no way to know its value is
// hostile-controlled: PR #100 / issue #93 hardened ~24 print sites by wrapping
// each in Sanitize, but nothing stopped the *next* Fprintf from printing a
// fetched id raw and silently reintroducing the escape-injection class. Text
// closes that gap structurally:
//
//   - Text implements fmt.Stringer, and its String() returns the SANITIZED
//     value. Because fmt's %s/%v/%q invoke String() for any value implementing
//     fmt.Stringer, a Text printed through fmt.Fprint* / fmt.Sprintf is
//     sanitized BY DEFAULT. A new print site of an untrusted field therefore
//     cannot reintroduce the #93 class by accident — the type sanitizes itself.
//   - The only way to obtain the raw, UNSANITIZED bytes is the explicit,
//     deliberately-alarming Unverified() method. Every call is a greppable
//     acknowledgement that the caller is bypassing display sanitization; it is
//     for non-display logic only (filesystem paths, map keys, comparisons,
//     re-serialization), never for building terminal output.
//
// Text is a defined string type (not a struct) on purpose: it serializes
// transparently through encoding/json and go-toml/v2 (a named string kind, so
// `omitempty` still elides an empty value and round-trip fidelity is preserved)
// and keeps the `==`/`!=`/`<` operators and map-key usability the canonical
// model relies on. The residual that a defined string type permits — a
// deliberate `string(t)` conversion to defeat the Stringer, then a raw print —
// is the same shape of *accepted residual* documented for secrets.Resolved
// (the lint fence there is likewise defeatable by a deliberate two-step). No
// innocent print produces it; Unverified() is the blessed, named unwrap.
//
// # Machine vs terminal contract
//
// MarshalText/JSON is NOT overridden: a Text marshals to its RAW value. That is
// deliberate — the `--json` surfaces are a machine contract where the consumer
// owns escaping, exactly as the per-site comments in the CLI already state. Raw
// in JSON, sanitized on the terminal, with no extra plumbing, falls out of the
// named-string design for free.
package untrusted

import (
	"strings"
	"unicode/utf8"
)

// Text is an untrusted string: marketplace/plugin/native metadata that must be
// sanitized before terminal display. Print it directly (its String() sanitizes)
// or reach Sanitize-applied text via String(); use Unverified() only for
// non-display logic. See the package doc for the full contract.
type Text string

// Wrap tags s as untrusted. Use it at the boundary where a plain string crossing
// in from outside the trust boundary (a fetched manifest field decoded into a
// plain string, an adapter-reported component name) first becomes Text.
func Wrap(s string) Text { return Text(s) }

// String returns the value sanitized for terminal display (control + deceptive
// format runes stripped). It satisfies fmt.Stringer, so fmt's %s/%v/%q sanitize
// a Text automatically — the safe-by-default property the type exists for.
func (t Text) String() string { return Sanitize(string(t)) }

// Unverified returns the RAW, UNSANITIZED bytes. The name is intentionally
// alarming: every call is an explicit acknowledgement that display sanitization
// is being bypassed. Use it ONLY for non-display logic (filesystem paths, map
// keys, comparisons, re-serialization) — never to build terminal output. To
// render a Text, print it directly or call String().
func (t Text) Unverified() string { return string(t) }

// Empty reports whether the underlying value is the empty string. Provided so
// callers read `t.Empty()` instead of comparing against a Text("") literal.
func (t Text) Empty() bool { return t == "" }

// Join concatenates the SANITIZED form of each Text with sep between elements —
// the Text-slice analogue of strings.Join, for building one display string from
// several untrusted values (e.g. the comma-separated plugin list in the
// status/doctor "undeclared native plugins" note). Each element is sanitized via
// String(), so the result is safe to print directly; do NOT wrap it in a second
// Sanitize. sep is trusted caller-supplied text and is emitted verbatim.
func Join(ts []Text, sep string) string {
	var b strings.Builder
	for i, t := range ts {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(t.String())
	}
	return b.String()
}

// Sanitize strips control characters and deceptive format runes from a string so
// untrusted text — a fetched marketplace plugin's id, a plugin-supplied component
// name — can be rendered to a terminal without smuggling escape sequences or
// spoofed/hidden text through it. It removes:
//
//   - C0 controls (0x00–0x1F, which includes ESC 0x1B, CR, LF, TAB, BEL, and
//     backspace), DEL (0x7F), and C1 controls (0x80–0x9F). Neutralizing the
//     ESC/CSI introducer is the security-relevant part: a name like "\x1b[31mX"
//     renders as the inert literal "[31mX" rather than recoloring the terminal,
//     clearing the screen, or spoofing subsequent rows.
//   - The explicit Unicode bidirectional formatting controls — the embedding/
//     override set U+202A–U+202E (LRE/RLE/PDF/LRO/RLO) and the isolate set
//     U+2066–U+2069 (LRI/RLI/FSI/PDI). These are the "Trojan Source"
//     (CVE-2021-42574) class: U+202E and friends can visually reorder a plugin
//     id so it reads as a trusted name while its bytes say otherwise.
//   - Zero-width / invisible format runes — U+200B–U+200D (ZWSP/ZWNJ/ZWJ) and
//     U+FEFF (zero-width no-break space / BOM) — which can hide characters or
//     invisibly pad a name.
//
// Printable text passes through unchanged, including non-ASCII letters and
// ordinary spaces. This deliberately leaves *implicit* bidi alone: ordinary
// right-to-left scripts (Arabic, Hebrew) and CJK get their direction from the
// letters themselves, not from the explicit override controls, so a legitimate
// non-Latin name survives byte-for-byte — only the explicit formatting controls
// an attacker would inject are removed. Apply Sanitize at the display boundary,
// before width/Pad calculation, so a stripped rune never throws off column
// alignment. (Text.String() applies it; ui.Sanitize delegates here for the
// composite/string-built display sites that don't hold a Text.)
//
// Scanning is rune-level (not byte-level): byte-level stripping of the 0x80–0x9F
// range would corrupt legitimate multibyte UTF-8 (a CJK rune's continuation
// bytes live there). Invalid UTF-8 is normalized rather than passed verbatim — a
// malformed byte decodes to U+FFFD and is re-emitted as U+FFFD, so a raw
// 0x80–0x9F byte (a C1 CSI introducer on an 8-bit terminal) can never survive.
//
// What Sanitize does NOT do: it does not normalize display *width*. Combining
// marks and wide/ambiguous-width runes still skew rune-counting alignment (see
// ui.Pad); that is a purely cosmetic limitation, not a spoofing vector this
// function targets.
func Sanitize(s string) string {
	// Fast path: the overwhelmingly common case is clean text, so scan once and
	// only allocate when there is something to change. We also rebuild on invalid
	// UTF-8 (RuneError) — `range` yields RuneError for a malformed byte, and the
	// rebuild's WriteRune normalizes it to U+FFFD, so a raw 0x80–0x9F byte (a C1
	// CSI introducer on an 8-bit terminal) can never pass through verbatim.
	clean := true
	for _, r := range s {
		if shouldStrip(r) || r == utf8.RuneError {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !shouldStrip(r) {
			b.WriteRune(r) // WriteRune(RuneError) emits U+FFFD, normalizing bad bytes
		}
	}
	return b.String()
}

// shouldStrip reports whether Sanitize removes r: either a terminal-control rune
// (isControl) or a deceptive format rune (isDeceptiveFormat).
func shouldStrip(r rune) bool {
	return isControl(r) || isDeceptiveFormat(r)
}

// isControl reports whether r is a C0 control (incl. ESC/CR/LF/TAB), DEL, or a
// C1 control — the runes that can carry terminal escape semantics.
func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// isDeceptiveFormat reports whether r is a printable-but-deceptive format rune
// that Sanitize strips: an explicit Unicode bidi formatting control (the
// embedding/override set U+202A–U+202E and the isolate set U+2066–U+2069 — the
// "Trojan Source" class) or a zero-width / invisible rune (U+200B–U+200D, U+FEFF).
// It deliberately excludes ordinary RTL/CJK letters and the benign directional
// marks (U+200E/U+200F LRM/RLM, U+061C ALM) — which only set the direction of
// adjacent neutrals and cannot reorder text the way the override/isolate controls
// can — so legitimate non-Latin names are preserved. Scope is the explicit bidi +
// zero-width spoofing set, not every default-ignorable or width-affecting rune:
// e.g. U+2028/U+2029 (line/paragraph separators), U+00AD (soft hyphen), and
// U+2060 (word joiner) are knowingly left alone — terminals don't act on them the
// way they do on CR/LF (which isControl already strips), and width skew is an
// accepted cosmetic limitation (see ui.Pad).
func isDeceptiveFormat(r rune) bool {
	switch {
	case r >= 0x202a && r <= 0x202e: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	case r >= 0x200b && r <= 0x200d: // ZWSP, ZWNJ, ZWJ
		return true
	case r == 0xfeff: // zero-width no-break space / BOM
		return true
	default:
		return false
	}
}
