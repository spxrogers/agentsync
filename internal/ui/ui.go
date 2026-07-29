// Package ui centralizes agentsync's terminal presentation: semantic color, a
// curated glyph vocabulary, a labeled diagnostic vocabulary, and small layout
// primitives (sections, status lines, aligned labels). It is the single place
// that decides whether to emit ANSI, so every command renders through a
// *Printer and the color/glyph/spacing language stays consistent across
// `status`, `diff`, `doctor`, and `apply`.
//
// Output splits into two kinds, and the distinction is the organizing rule of
// this package:
//
//   - A DIAGNOSTIC is a notice *about* the run — an error, a warning, an
//     informational aside. Every one carries a level label (`✗ ERROR`,
//     `⚠ WARN`, `ℹ INFO`) and goes to stderr. See diag.go for the vocabulary
//     and slog.go for the handler that gives library-side slog calls the same
//     shape.
//   - RESULT output is what the command was asked to produce — a status table,
//     a diff, a `--json` payload, a list, or the one-line outcome of a mutating
//     command. It carries no level label. Its success line instead leads with a
//     curated emoji (`✅ added agent: claude`), because labeling an outcome
//     "INFO" is noise that dilutes the labels that matter.
//
// Two independent axes govern how any of it is styled:
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

	"golang.org/x/term"

	"github.com/spxrogers/agentsync/internal/untrusted"
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
	GlyphInfo    = "•" // neutral bullet, inside a report body
	GlyphArrow   = "→" // transition / "see"

	// The two glyphs below belong to the DIAGNOSTIC label vocabulary in diag.go
	// rather than to report layout, but they live here so the whole curated set
	// is visible in one place.
	//
	// GlyphBullet is the same rune as GlyphInfo and that is deliberate: a faint
	// bullet is the right mark for a DEBUG label and for a list item alike. They
	// are named separately so a future change to one cannot silently move the
	// other — the failure mode a shared constant invites.
	GlyphNote   = "ℹ" // INFO label
	GlyphBullet = "•" // DEBUG label
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
// invocation via New; the color decisions are frozen at construction.
type Printer struct {
	Out io.Writer
	Err io.Writer
	// color and colorErr are resolved SEPARATELY, one per stream. `auto` asks
	// whether the destination is a terminal, and Out and Err are different
	// destinations: `agentsync apply 2>err.log` run from a terminal has a TTY
	// stdout and a file stderr. Deciding once off Out and reusing it for Err
	// wrote ANSI into that file — precisely what the package doc promises never
	// happens. Every diagnostic goes to Err, so this is not a corner case; it is
	// the common path for ~170 emission sites and for SlogHandler.
	color    bool
	colorErr bool
	mode     ColorMode
}

// New builds a Printer bound to out/err, resolving whether to emit color from
// mode, the NO_COLOR environment variable, and whether each writer is a
// terminal. The two streams are resolved independently — see the struct doc.
func New(out, err io.Writer, mode ColorMode) *Printer {
	return &Printer{
		Out:      out,
		Err:      err,
		color:    resolveColor(out, mode),
		colorErr: resolveColor(err, mode),
		mode:     mode,
	}
}

// styleFor returns a view of p whose color decision matches the stream w. Used
// by the diagnostic writers, which take an explicit io.Writer and must not
// inherit the wrong stream's TTY-ness.
//
// Interface comparison is exact (dynamic type + value), so this recognizes
// p.Out/p.Err and nothing else. An UNRECOGNIZED writer falls back to the Err
// decision: every caller passing a third writer today is emitting a diagnostic,
// and Err is the conservative choice for one (a diagnostic reaching an unknown
// sink is more likely redirected than interactive).
func (p *Printer) styleFor(w io.Writer) *Printer {
	// Out is the only stream whose decision differs from the diagnostic default,
	// so it is the only case worth naming. Everything else — Err and any
	// unrecognized writer — takes the Err decision.
	if w == p.Out || p.color == p.colorErr {
		return p // already the right decision, or the two streams agree
	}
	return &Printer{Out: p.Out, Err: p.Err, color: p.colorErr, colorErr: p.colorErr, mode: p.mode}
}

// ColorMode returns the requested mode this Printer was built with — NOT the
// resolved boolean (that is Color). The distinction matters for a caller that
// must rebuild a Printer against different writers and honor the same user
// intent: `auto` has to be re-resolved against the NEW writer's TTY-ness, so
// carrying the resolved bool across would be wrong. ReportError does exactly
// this, rebuilding against os.Stderr after cobra has returned.
func (p *Printer) ColorMode() ColorMode { return p.mode }

// Color reports whether this Printer emits ANSI on Out. Commands that hand a
// writer to a third-party renderer (e.g. the diff library's own colorizer)
// consult this to gate that output through the same decision — and every such
// caller today renders to Out, which is why this reports the Out decision.
func (p *Printer) Color() bool { return p.color }

// ColorErr reports whether this Printer emits ANSI on Err — the stream every
// diagnostic uses. Distinct from Color; see the Printer struct doc.
func (p *Printer) ColorErr() bool { return p.colorErr }

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

// Sanitize strips control characters and deceptive format runes from a string
// so untrusted text can be rendered to a terminal without smuggling escape
// sequences or spoofed/hidden text. It is a thin re-export of
// untrusted.Sanitize (which owns the implementation and its full doc) for the
// display sites that hold a plain composite/built string rather than an
// untrusted.Text — a Text sanitizes itself via its String() method, so prefer
// printing a Text directly. Apply at the display boundary, before width/Pad
// calculation, so a stripped rune never throws off column alignment.
func Sanitize(s string) string { return untrusted.Sanitize(s) }

// WarnWriter wraps a destination writer and rewrites "warning: " line prefixes
// into the shared WARN diagnostic label (see diag.go) so every warning —
// whether emitted by the CLI itself, by an adapter's Ingest, or by capture's
// re-reference path — is byte-identical to a p.Warnf and to a slog.Warn from
// the render pipeline. Lines that do not start with the literal "warning: "
// prefix (e.g. pre-styled ANSI lines, indented continuation lines, or already-
// labeled diagnostics) pass through verbatim. The writer is line-buffered so a
// callers' partial Write is held until a newline arrives — fmt.Fprintf in
// practice always finishes a line per call, but buffering keeps a chunked
// writer correct.
//
// The "warning: " sentinel stays the emitter-side contract because the adapter
// packages must not depend on ui: an adapter writes a plain prefixed line into
// an io.Writer it was handed, and the styling happens here, at the one place
// that knows whether this terminal gets color.
//
// Not safe for concurrent use: the line-assembly buffer is unsynchronized.
// One *WarnWriter per command invocation is the intended pattern.
type WarnWriter struct {
	w   io.Writer
	p   *Printer
	buf []byte
	// partial records that the last emit wrote a line with no trailing newline
	// (only Flush can do that), so EndLine knows whether to terminate it.
	partial bool
}

// NewWarnWriter returns a *WarnWriter that flushes styled lines to w using p.
// p's color decision is honored: with color off, the prefix degrades to a plain
// "⚠ WARN" (the glyph and the level word are content, not decoration — same
// rule as the curated glyph vocabulary above).
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
		s.partial = true
	}
}

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
	fmt.Fprintf(s.w, "%s  %s", LevelWarn.Label(s.p), rest)
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

// EndLine terminates a partial line if one was just flushed, so a subsequent
// writer to the same stream starts at column 0.
//
// Flush drains whatever incomplete line the buffer held, by definition without a
// trailing newline — and the next thing written to that stream may come from a
// different layer entirely (main's terminal ✗ ERROR line, say), which would then
// render on the same row as the fragment. Pairing Flush with EndLine keeps the
// two visually separate. A no-op unless the last emitted byte was a partial line.
func (s *WarnWriter) EndLine() {
	if s.partial {
		fmt.Fprintln(s.w)
		s.partial = false
	}
}
