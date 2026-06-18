// Package ui centralizes agentsync's terminal presentation: semantic color, a
// curated glyph vocabulary, and small layout primitives (sections, status
// lines, aligned labels). It is the single place that decides whether to emit
// ANSI, so every command renders through a *Printer and the color/glyph/spacing
// language stays consistent across `status`, `diff`, `doctor`, and `apply`.
//
// Two independent axes:
//
//   - Color is TTY-gated. `--color=always|never` forces it; `auto` (the
//     default) enables color only when the output is a terminal and NO_COLOR
//     (https://no-color.org) is unset. Non-TTY output (pipes, files, tests) is
//     therefore byte-for-byte plain — color never leaks into a redirect.
//   - Glyphs are always Unicode. The ✓ / ◐ / ✗ vocabulary already appears in the
//     translation report and the capability matrix; keeping it unconditional
//     means piped output reads the same as the screen and existing fixtures
//     hold. Color, not glyph choice, is what degrades.
//
// Color is reserved for state: a green ✓ means synced, a red ✗ means drift. It
// is never decoration. Everything still parses with color stripped.
package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// ANSI SGR codes, restricted to the basic 16-color palette so any terminal
// that supports color at all renders them faithfully — semantic status
// coloring never needs 256-color or truecolor.
const (
	codeReset  = "\x1b[0m"
	codeBold   = "\x1b[1m"
	codeFaint  = "\x1b[2m"
	codeRed    = "\x1b[31m"
	codeGreen  = "\x1b[32m"
	codeYellow = "\x1b[33m"
	codeBlue   = "\x1b[34m"
	codeCyan   = "\x1b[36m"
)

// Curated glyph vocabulary. Always Unicode; each is one display column wide, so
// callers can align around them with plain rune/space counting (no runewidth).
const (
	GlyphOK      = "✓" // success / synced / clean
	GlyphPartial = "◐" // partial coverage (mirrors the capability matrix)
	GlyphErr     = "✗" // failure / drift / missing
	GlyphWarn    = "⚠" // warning / needs attention
	GlyphInfo    = "•" // neutral bullet
	GlyphArrow   = "→" // transition / "see"
)

// ColorMode is the resolved value of the global --color flag.
type ColorMode int

const (
	ColorAuto ColorMode = iota
	ColorAlways
	ColorNever
)

// ParseColorMode maps the --color flag string to a ColorMode. An empty string
// defaults to auto so callers can pass the raw flag value.
func ParseColorMode(s string) (ColorMode, error) {
	switch s {
	case "", "auto":
		return ColorAuto, nil
	case "always":
		return ColorAlways, nil
	case "never":
		return ColorNever, nil
	default:
		return ColorAuto, fmt.Errorf("unknown --color value %q; want auto, always, or never", s)
	}
}

// Printer renders styled output to a pair of writers. Construct one per command
// invocation via New; the color decision is frozen at construction.
type Printer struct {
	Out   io.Writer
	Err   io.Writer
	color bool
}

// New builds a Printer bound to out/err, resolving whether to emit color from
// mode, the NO_COLOR environment variable, and whether out is a terminal.
func New(out, err io.Writer, mode ColorMode) *Printer {
	return &Printer{Out: out, Err: err, color: resolveColor(out, mode)}
}

// Color reports whether this Printer emits ANSI. Commands that hand a writer to
// a third-party renderer (e.g. the diff library's own colorizer) consult this
// to gate that output through the same decision.
func (p *Printer) Color() bool { return p.color }

func resolveColor(out io.Writer, mode ColorMode) bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	default:
		// NO_COLOR: any value, even empty, disables color per the standard.
		if _, ok := os.LookupEnv("NO_COLOR"); ok {
			return false
		}
		return isTerminal(out)
	}
}

// isTerminal reports whether w is backed by a terminal. A *bytes.Buffer / pipe
// (tests, redirects) has no Fd and is therefore plain — which is exactly what
// keeps captured-output tests byte-stable.
func isTerminal(w io.Writer) bool {
	type fdWriter interface{ Fd() uintptr }
	f, ok := w.(fdWriter)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func (p *Printer) wrap(code, s string) string {
	if !p.color || s == "" {
		return s
	}
	return code + s + codeReset
}

// Semantic style helpers. Each returns s unchanged when color is disabled, so
// callers can compose them freely without branching.
func (p *Printer) Bold(s string) string   { return p.wrap(codeBold, s) }
func (p *Printer) Faint(s string) string  { return p.wrap(codeFaint, s) }
func (p *Printer) Red(s string) string    { return p.wrap(codeRed, s) }
func (p *Printer) Green(s string) string  { return p.wrap(codeGreen, s) }
func (p *Printer) Yellow(s string) string { return p.wrap(codeYellow, s) }
func (p *Printer) Blue(s string) string   { return p.wrap(codeBlue, s) }
func (p *Printer) Cyan(s string) string   { return p.wrap(codeCyan, s) }

// Section prints a heading (bold when colored, plain text otherwise) to Out.
func (p *Printer) Section(title string) {
	fmt.Fprintln(p.Out, p.Bold(title))
}

// Pad left-justifies s to a fixed visible width, counting runes (the glyph set
// is single-width) rather than bytes, then returns the padded plain string.
// Callers color the RESULT so that ANSI bytes never throw off the column —
// padding is applied before any escape codes exist.
//
// Alignment is best-effort for non-ASCII: counting is per-rune, so wide/
// ambiguous-width runes (CJK, emoji) and combining marks make the rune count
// diverge from the actual display-cell width and skew the column. This is a
// deliberate, purely cosmetic limitation — bringing in a grapheme-aware width
// table (golang.org/x/text/width) was judged not worth the dependency for the
// curated, mostly-ASCII output here. Sanitize removes the security-relevant
// deceptive runes (bidi/zero-width) but does not normalize width.
func Pad(s string, width int) string {
	n := 0
	for range s {
		n++
	}
	if n >= width {
		return s
	}
	return s + spaces(width-n)
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
// alignment.
//
// Scanning is rune-level (not byte-level): byte-level stripping of the 0x80–0x9F
// range would corrupt legitimate multibyte UTF-8 (a CJK rune's continuation
// bytes live there). Invalid UTF-8 is normalized rather than passed verbatim — a
// malformed byte decodes to U+FFFD and is re-emitted as U+FFFD, so a raw
// 0x80–0x9F byte (a C1 CSI introducer on an 8-bit terminal) can never survive.
//
// What Sanitize does NOT do: it does not normalize display *width*. Combining
// marks and wide/ambiguous-width runes still skew the rune-counting alignment of
// Pad (and of the caller-side column counting in internal/cli's explain output);
// that is a purely cosmetic limitation, documented on Pad, not a spoofing vector
// this function targets.
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
// can — so legitimate non-Latin names are preserved. Scope is the explicit bidi + zero-width spoofing set, not every
// default-ignorable or width-affecting rune: e.g. U+2028/U+2029 (line/paragraph
// separators), U+00AD (soft hyphen), and U+2060 (word joiner) are knowingly left
// alone — terminals don't act on them the way they do on CR/LF (which isControl
// already strips), and width skew is an accepted cosmetic limitation (see Pad).
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

// WarnWriter wraps a destination writer and styles "warning: " line prefixes
// as a bold-yellow "⚠️ warning:" so every warning — whether emitted by the
// CLI itself, by an adapter's Ingest, or by capture's re-reference path —
// reads consistently. Lines that do not start with the literal "warning: "
// prefix (e.g. pre-styled ANSI lines, indented continuation lines, or
// "agentsync:" notes) pass through verbatim. The writer is line-buffered so a
// callers' partial Write is held until a newline arrives — fmt.Fprintf in
// practice always finishes a line per call, but buffering keeps a chunked
// writer correct.
//
// Not safe for concurrent use: the line-assembly buffer is unsynchronized.
// One *WarnWriter per command invocation is the intended pattern.
type WarnWriter struct {
	w   io.Writer
	p   *Printer
	buf []byte
}

// NewWarnWriter returns a *WarnWriter that flushes styled lines to w using p.
// p's color decision is honored: with color off, the prefix becomes a plain
// "⚠️ warning:" (the glyph is content, not decoration — same rule as the
// curated glyph vocabulary above).
func NewWarnWriter(w io.Writer, p *Printer) *WarnWriter {
	return &WarnWriter{w: w, p: p}
}

const warnLinePrefix = "warning: "

// Write line-buffers data, emitting completed lines through emit. Partial
// trailing bytes are retained for the next Write. Always returns len(p), nil
// (the contract callers like fmt.Fprintf expect).
func (s *WarnWriter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		idx := bytes.IndexByte(s.buf, '\n')
		if idx < 0 {
			break
		}
		s.emit(s.buf[:idx+1])
		s.buf = s.buf[idx+1:]
	}
	return len(p), nil
}

// Flush emits any buffered partial line (no trailing \n) as-is. Call at end of
// command if you've routed a writer that may not always end in \n; the import
// path does always terminate, so this is defensive.
func (s *WarnWriter) Flush() {
	if len(s.buf) > 0 {
		s.emit(s.buf)
		s.buf = nil
	}
}

// GlyphWarnEmoji is the colourful warning sign (with VS16) used as the warning
// label prefix. Wider than one column in some terminals, which is fine — the
// warning lines are not part of any padded layout.
const GlyphWarnEmoji = "⚠️"

// stderrSetter is the structural shape of adapter.WarnEmitter. Duplicated
// here so ui doesn't depend on the adapter package; each concrete adapter's
// test suite pins itself against adapter.WarnEmitter at compile time, and
// internal/cli/import_warn_routing_test.go pins a real adapter through
// RouteTo at runtime, so drift between the two definitions fails the
// build or the test rather than silently regressing to a no-op.
//
// TODO: when WarnEmitter grows a second method (a structured-diagnostic
// sink, a `Verbose(io.Writer)`, etc.), the structural-duplicate-with-
// compile-pin pattern stops being free. Move the interface to a neutral
// package (e.g. internal/cliio) so ui and adapter both import one
// definition.
type stderrSetter interface{ SetStderr(w io.Writer) }

// RouteTo wires this writer into anything that exposes a
// SetStderr(io.Writer) setter (matching adapter.WarnEmitter) and returns a
// restore function that detaches the writer when invoked. Idiomatic use
// pairs with defer:
//
//	defer warnW.RouteTo(a)()
//
// The inner RouteTo(a) call evaluates immediately (wires the writer); the
// outer () is the deferred restore. The returned function is always
// safe to call — it's a no-op when the target doesn't implement the
// setter, when the target is a typed-nil pointer, or when the target was
// an untyped nil — so callers never need to type-assert or nil-check.
//
// Non-implementor cases that resolve to a silent no-op:
//
//   - untyped nil (`any(nil)`): the type-assert misses because the
//     interface value carries no concrete type.
//   - typed nil (`var a *T = nil; RouteTo(a)`): the type-assert SUCCEEDS
//     because the interface value holds the method set of *T, but calling
//     SetStderr would dereference the nil pointer. RouteTo guards
//     against this via reflect.
//   - any value whose dynamic type doesn't implement SetStderr.
func (s *WarnWriter) RouteTo(a any) func() {
	v, ok := s.setterOf(a)
	if !ok {
		return noopRestore
	}
	v.SetStderr(s)
	return func() { v.SetStderr(nil) }
}

// noopRestore is the restore function returned when RouteTo can't wire
// anything. Shared so the non-implementor path doesn't allocate per
// call. Keep stateless: a future addition that captures state would
// silently share that state across every no-op RouteTo call (every
// command that runs against the noop adapter, every nil-setter test).
var noopRestore = func() {}

// setterOf returns the SetStderr setter on a, or (nil, false) when a
// cannot be safely called. The three rejection paths are distinct:
//
//  1. `a == nil`: an UNTYPED nil any-value (no concrete type behind it).
//     The bare `nil` check catches this; the later type-assert would
//     also fail, but doing it explicitly documents the case.
//  2. `a.(stderrSetter)` failure: a's dynamic type doesn't implement
//     SetStderr — this is the "noop adapter" path. Today's caller
//     can pass any adapter.Adapter; non-implementors get this path.
//  3. `rv.IsNil()` on a Pointer kind: a TYPED-nil pointer (e.g.
//     `var p *Adapter = nil`). The interface value carries *Adapter's
//     method set, so the type-assert in (2) SUCCEEDS, but calling
//     SetStderr on the nil pointer would dereference and panic. The
//     reflect guard is the only check that catches this.
//
// Scope of the reflect guard: Pointer kind only. Map/Chan/Func/Slice/
// Interface kinds can also be typed-nil and would panic similarly, but
// no implementor today (all *Adapter) uses them, and the unidiomatic
// shape would be a louder review signal than a runtime no-op. Widen
// the guard if a non-pointer implementor ever lands.
func (s *WarnWriter) setterOf(a any) (stderrSetter, bool) {
	if a == nil {
		return nil, false
	}
	v, ok := a.(stderrSetter)
	if !ok {
		return nil, false
	}
	rv := reflect.ValueOf(a)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil, false
	}
	return v, true
}

func (s *WarnWriter) emit(line []byte) {
	if !bytes.HasPrefix(line, []byte(warnLinePrefix)) {
		_, _ = s.w.Write(line)
		return
	}
	rest := line[len(warnLinePrefix):]
	label := s.p.Yellow(s.p.Bold(GlyphWarnEmoji + " warning:"))
	fmt.Fprintf(s.w, "%s %s", label, rest)
}

func spaces(n int) string {
	const blanks = "                                                                "
	if n <= 0 {
		return ""
	}
	if n <= len(blanks) {
		return blanks[:n]
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = ' '
	}
	return string(out)
}
