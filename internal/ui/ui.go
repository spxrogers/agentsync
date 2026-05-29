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

// WarnWriter wraps a destination writer and styles "warning: " line prefixes
// as a bold-yellow "⚠️ warning:" so every warning — whether emitted by the
// CLI itself, by an adapter's Ingest, or by capture's re-reference path —
// reads consistently. Lines that do not start with the literal "warning: "
// prefix (e.g. pre-styled ANSI lines, indented continuation lines, or
// "agentsync:" notes) pass through verbatim. The writer is line-buffered so a
// callers' partial Write is held until a newline arrives — fmt.Fprintf in
// practice always finishes a line per call, but buffering keeps a chunked
// writer correct.
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

// RouteTo wires the writer into anything that exposes a `SetStderr(io.Writer)`
// setter (matching adapter.WarnSink, though ui does not import the adapter
// package — the interface is structural). Adapters that don't implement the
// setter are silently no-ops, so callers don't have to type-assert before
// calling this; pass any adapter.Adapter and let the structural match decide.
//
// Typical use from a CLI command:
//
//	warnW := ui.NewWarnWriter(p.Err, p)
//	warnW.RouteTo(a) // a is the Adapter returned from registry.Lookup
//	c, err := a.Ingest(...)
//
// Every "warning: …" line the adapter writes during Ingest then picks up the
// bold-yellow "⚠️ warning:" styling, identical to warnings the command emits
// itself.
func (s *WarnWriter) RouteTo(a any) {
	type setter interface{ SetStderr(io.Writer) }
	if v, ok := a.(setter); ok {
		v.SetStderr(s)
	}
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
