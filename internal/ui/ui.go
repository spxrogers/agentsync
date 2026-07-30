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
	"strings"

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
	}
}

// styleForDiag returns a view of p whose color decision matches the stream w, for
// DIAGNOSTIC output. An unrecognized writer takes the Err decision: a diagnostic
// reaching an unknown sink is more likely redirected than interactive.
func (p *Printer) styleForDiag(w io.Writer) *Printer {
	if sameWriter(w, p.Out) {
		return p.withColor(p.color)
	}
	return p.withColor(p.colorErr)
}

// styleForResult returns a view of p whose color decision matches the stream w,
// for RESULT output (a success line). The fallback is the mirror image of
// styleForDiag's: an unrecognized writer takes the OUT decision, because a
// result line's natural home is stdout — Fsuccessf's doc invites a
// caller-supplied buffer, and giving that stderr's TTY-ness would be backwards.
func (p *Printer) styleForResult(w io.Writer) *Printer {
	if sameWriter(w, p.Err) {
		return p.withColor(p.colorErr)
	}
	return p.withColor(p.color)
}

// withColor returns p when the requested decision already matches (the common
// case, and it avoids an allocation per output line), otherwise a shallow copy.
func (p *Printer) withColor(color bool) *Printer {
	if p.color == color {
		return p
	}
	return &Printer{Out: p.Out, Err: p.Err, color: color, colorErr: p.colorErr}
}

// sameWriter reports whether two io.Writer values are the same writer.
//
// Not a bare `a == b`: comparing interface values panics when the dynamic type is
// uncomparable (a struct holding a slice, map, or func), and this runs on every
// diagnostic line — the worst place for a panic is while reporting an error.
//
// reflect.VALUE.Comparable(), NOT reflect.TYPE.Comparable() — easy to get
// backwards, and the whole correctness of this function. `struct{ io.Writer }` is
// statically comparable (an interface field's comparability is only decidable from
// what it holds), so a type-level check lets a func-holding writer through and
// panics on `==`. The value-level check inspects the actual contents.
//
// Different dynamic types can never be equal, so a type mismatch short-circuits.
// An uncomparable value reports "not the same writer", degrading to the caller's
// documented fallback rather than crashing.
func sameWriter(a, b io.Writer) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Type() != vb.Type() || !va.Comparable() || !vb.Comparable() {
		return false
	}
	return a == b
}

// Color reports whether this Printer emits ANSI ON OUT. Every diagnostic goes to
// Err, whose decision is resolved separately (see the struct doc) and reached
// internally via styleForDiag — so this is deliberately NOT "does this Printer
// use color". Commands that hand a writer to a third-party renderer (e.g. the
// diff library's own colorizer) consult this to gate that output through the same
// decision, and every such caller renders to Out.
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
		return terminalCheck(out)
	}
}

// terminalCheck is the TTY probe resolveColor uses, indirected through a variable
// so a test can make the two streams answer DIFFERENTLY. Nothing else can: two
// in-memory buffers never diverge under `auto`, and the forced modes force both the
// same way. See TestNewResolvesColorPerStreamAndDoesNotSwapThem for what that
// buys, including why counting probes is not enough.
var terminalCheck = isTerminal

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

// Flush drains any buffered partial line, TERMINATING it. Call at end of command
// if you've routed a writer that may not always end in \n; every emitter today
// does terminate its lines, so this is defensive.
func (s *WarnWriter) Flush() {
	if len(s.buf) == 0 {
		return
	}
	// Terminate here rather than leaving it to the caller. A buffered partial line
	// has no trailing newline by definition, so whatever writes to this stream next
	// — main's terminal ✗ ERROR line, typically — lands on the same row. Making the
	// primitive correct beats a two-call protocol callers must remember.
	if s.buf[len(s.buf)-1] != '\n' {
		s.buf = append(s.buf, '\n')
	}
	s.emit(s.buf)
	s.buf = nil
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
	// Sanitize the BODY. This writer's input comes from the adapter and capture
	// packages, which cannot call ui.Sanitize themselves — they must not import ui,
	// which is the whole reason the "warning: " sentinel exists — so if ui does not
	// sanitize here, nothing in the pipeline does.
	//
	// It is a backstop, not a replacement for %q at the emitters. A convention every
	// future emitter has to remember is weaker than a chokepoint, and %q does not
	// cover the %v-of-error sites at all — claude/ingest.go:88 quotes e.Name() with
	// %q and then prints %v of an error whose *fs.PathError re-embeds that same
	// ReadDir name RAW (the hazard untrusted.go:37 names, handled by hand in
	// secrets/age.go, cli/doctor.go and cli/check.go). This line is what catches it
	// here. See "Who sanitizes" in docs/components.md.
	//
	// Only the labeled branch is sanitized. The passthrough branch above stays
	// verbatim because it carries OUR OWN already-styled lines (importIO's INFO
	// notes), whose ANSI this would otherwise strip.
	//
	// That asymmetry bounds the backstop, and the bound is easy to overstate: Write
	// splits on '\n' BEFORE emit, so this body never holds an interior newline and
	// Sanitize's newline-stripping cannot fire. An emitter interpolating a raw
	// newline lands lines 2..n in the passthrough branch — unlabeled, unsanitized —
	// which is why %q at the emitters is still load-bearing for THAT case.
	// Pre-existing; do not read this Sanitize as covering it.
	//
	// styleForDiag, not s.p directly: this writer is constructed over p.Err, so
	// taking the Out decision wrote ANSI into `agentsync import 2>err.log`.
	body, nl := string(rest), ""
	if strings.HasSuffix(body, "\n") {
		body, nl = strings.TrimSuffix(body, "\n"), "\n"
	}
	fmt.Fprintf(s.w, "%s  %s%s", LevelWarn.Label(s.p.styleForDiag(s.w)), Sanitize(body), nl)
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

// stripSGR removes ANSI CSI escape sequences from s, leaving everything else —
// including newlines — untouched.
//
// Callers legitimately pre-style the text they hand a diagnostic writer (the
// upgrade-notice banner composes p.Bold/p.Yellow fragments; gitbackup bolds
// "local-only"). Those helpers resolve color from p.color — the OUT decision —
// because a style helper cannot know which stream its result reaches. So
// `apply 2>err.log` from a terminal gave a plain LABEL followed by a body still
// carrying \x1b[1m.
//
// The diagnostic writers therefore strip SGR from the body when the target stream
// renders plain, instead of asking every call site to know its own stream. If color
// is off there, ANY escape in the body is unwanted, so nothing a caller wanted is
// lost. Newlines survive because indentContinuation needs them — which is also why
// this is not just Sanitize, whose stripping of newlines would defeat that. The
// diagnostic writers deliberately do not sanitize at all: see the "Who sanitizes"
// note in docs/components.md for which sites do and why.
func stripSGR(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s // overwhelmingly the common case; no allocation
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		// CSI: ESC '[' <params> <final byte in @..~>
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < '@' || s[j] > '~') {
				j++
			}
			if j < len(s) {
				i = j + 1 // skip the final byte too
				continue
			}
			break // unterminated: drop the remainder rather than emit a bare ESC
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// bodyFor prepares a caller-supplied message for the stream w: SGR is stripped when
// that stream renders without color, then continuation lines are aligned.
func (p *Printer) bodyFor(style *Printer, msg string) string {
	if !style.color {
		msg = stripSGR(msg)
	}
	return indentContinuation(msg)
}
