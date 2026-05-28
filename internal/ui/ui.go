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
