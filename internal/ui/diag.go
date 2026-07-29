package ui

import (
	"fmt"
	"io"
	"strings"
)

// Diagnostic severity levels. These are the ONLY severities agentsync renders;
// they map 1:1 onto slog's levels so a slog.Warn from deep in the render
// pipeline and a hand-written p.Warnf in a command produce byte-identical
// lines (see slog.go).
//
// Every diagnostic — anything that is a *notice about* the run rather than the
// run's own output — carries one. That is the whole point of the vocabulary:
// #211 shipped a fatal error as an unlabeled flat line directly under a WARN,
// and the eye had no way to tell which was which. A level word plus a glyph
// gives it two.
type Level int

// LevelDebug is deliberately the ZERO VALUE: a `var l Level` or a `Diagf(0, …)`
// then renders as the least-severe level rather than silently claiming ERROR.
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// valid reports whether l is one of the four defined levels. Out-of-range values
// are rendered as DEBUG (see glyph/word), so this exists to let String() stay
// honest about a value it cannot name.
func (l Level) valid() bool { return l >= LevelDebug && l <= LevelError }

// Rendered label geometry. Each label is `<glyph> <WORD padded to 5>`, giving a
// constant 7-column label followed by a 2-column gutter — so message text always
// starts at column 9 and wrapped/continuation lines can be indented to match.
//
// The level words are DEBUG/ERROR (5) and INFO/WARN (4), so 5 is the exact
// natural width; the padding costs nothing and makes the column explicit.
const (
	levelWordWidth = 5
	// diagIndent is the blank prefix that aligns a continuation line under the
	// message column of a labeled diagnostic. Unexported on purpose: Detailf /
	// Fdetailf are the seam for a continuation line, and an exported string of
	// literal spaces invites callers to hand-indent and drift out of alignment
	// when the label geometry changes.
	diagIndent = "         " // 7 (label) + 2 (gutter)
)

// glyph returns the label glyph for a level. The glyphs themselves are declared
// with the rest of the curated vocabulary in ui.go; see GlyphBullet's doc there
// for why DEBUG's mark is named separately from the report-body bullet.
func (l Level) glyph() string {
	switch l {
	case LevelError:
		return GlyphErr
	case LevelWarn:
		return GlyphWarn
	case LevelInfo:
		return GlyphNote
	default:
		return GlyphBullet
	}
}

// word returns the uppercase level word for a level.
func (l Level) word() string {
	switch l {
	case LevelError:
		return "ERROR"
	case LevelWarn:
		return "WARN"
	case LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

// String implements fmt.Stringer with the rendered level word, so a Level can be
// dropped into a message or a test failure without a switch.
//
// An out-of-range value reports itself as such rather than as "DEBUG". word()
// has to pick SOME label to render, and picks the least-severe one; String is
// where a test failure or a log line gets to say "this value has no name",
// which is the difference between a confusing diagnostic and a silent lie.
func (l Level) String() string {
	if !l.valid() {
		return fmt.Sprintf("Level(%d)", int(l))
	}
	return l.word()
}

// colorize applies the level's semantic color via p. Color is bold so the label
// separates from the message even on a busy screen; with color off the glyph and
// the word carry the whole signal, which is why the glyph is unconditional.
//
// The bold+color codes are emitted through ONE wrap call rather than nested
// p.Bold(p.Red(…)) calls. That still opens two SGR sequences (\x1b[1m\x1b[31m)
// — SGR has no single bold-and-red parameter — but it emits ONE reset instead of
// two, which is what nesting was actually costing.
func (l Level) colorize(p *Printer, s string) string {
	switch l {
	case LevelError:
		return p.wrap(codeBold+codeRed, s)
	case LevelWarn:
		return p.wrap(codeBold+codeYellow, s)
	case LevelInfo:
		return p.wrap(codeBold+codeCyan, s)
	default:
		return p.wrap(codeFaint, s)
	}
}

// Label renders the styled, fixed-width label for a level — glyph, space, then
// the level word padded to levelWordWidth. Padding is computed on the plain word
// and color applied to the result, so ANSI bytes never enter the width
// calculation (the same discipline Pad's doc describes).
//
// Exported so a test can construct the exact prefix it expects rather than
// hardcoding a literal that silently rots when the vocabulary changes.
func (l Level) Label(p *Printer) string {
	return l.colorize(p, l.glyph()+" "+Pad(l.word(), levelWordWidth))
}

// Success emoji vocabulary. Success lines deliberately carry NO level word: an
// "INFO" on "added agent: claude" is noise, and the emoji already says both
// "this worked" and "this is the outcome line, not a diagnostic". Each is a
// default-emoji-presentation rune (no variation selector), so terminals render
// them consistently without a VS16 width surprise.
//
// Pick by what happened, not by which command ran — `mcp remove` and
// `agent purge` are both EmojiRemoved.
const (
	EmojiSuccess  = "✅" // generic: added / set / saved / validated / passed
	EmojiApplied  = "🎉" // an apply wrote changes
	EmojiRemoved  = "🧹" // removed / purged / pruned
	EmojiImported = "📥" // captured destination → canonical source
	EmojiReverted = "🔙" // rolled back to a checkpoint
	EmojiInit     = "✨" // a new home or project tree was created
)

// Errorf writes a labeled ERROR diagnostic to Err.
func (p *Printer) Errorf(format string, args ...any) { p.Diagf(LevelError, format, args...) }

// Warnf writes a labeled WARN diagnostic to Err.
func (p *Printer) Warnf(format string, args ...any) { p.Diagf(LevelWarn, format, args...) }

// Infof writes a labeled INFO diagnostic to Err.
//
// Diagnostics go to stderr — including INFO — so that a command's actual output
// (a status table, a `--json` payload, a list) stays cleanly redirectable. An
// informational line that is part of the *result* is not a diagnostic and should
// be printed to Out directly or via Successf.
func (p *Printer) Infof(format string, args ...any) { p.Diagf(LevelInfo, format, args...) }

// Diagf writes a labeled diagnostic at l to Err.
func (p *Printer) Diagf(l Level, format string, args ...any) {
	p.Fdiagf(p.Err, l, format, args...)
}

// Fdiagf writes a labeled diagnostic at l to an explicit writer. Use it only
// when a diagnostic must land on a stream other than Err — routing a warning
// into a command's own report body, say. Prefer Diagf.
//
// Embedded newlines in the formatted message are indented to the message column
// so a multi-line diagnostic reads as one block rather than one labeled line
// followed by orphaned text at column 0.
func (p *Printer) Fdiagf(w io.Writer, l Level, format string, args ...any) {
	fmt.Fprintf(w, "%s  %s\n", l.Label(p.styleFor(w)), indentContinuation(fmt.Sprintf(format, args...)))
}

// Detailf writes an unlabeled continuation line to Err, indented to the message
// column of the diagnostic above it. Use for the supporting detail of a
// diagnostic — an enumerated list, a remedy, a path — so the block hangs
// together under its label.
func (p *Printer) Detailf(format string, args ...any) {
	p.Fdetailf(p.Err, format, args...)
}

// Fdetailf is Detailf against an explicit writer.
func (p *Printer) Fdetailf(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "%s%s\n", diagIndent, indentContinuation(fmt.Sprintf(format, args...)))
}

// Successf writes a success line to Out: the emoji, then the message, green
// when color is on. No level word — see the emoji vocabulary above.
func (p *Printer) Successf(emoji, format string, args ...any) {
	p.Fsuccessf(p.Out, emoji, format, args...)
}

// Fsuccessf is Successf against an explicit writer, for commands that stream
// their result into a caller-supplied buffer.
func (p *Printer) Fsuccessf(w io.Writer, emoji, format string, args ...any) {
	// No indentContinuation here, deliberately: a success line is a single
	// outcome, and there is no label column for a continuation to hang under.
	// A multi-line success message is a call-site bug, not something to lay out.
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(w, "%s %s\n", emoji, p.styleFor(w).Green(msg))
}

// indentContinuation aligns every line after the first to the message column.
// Blank lines are left blank rather than padded to trailing whitespace.
func indentContinuation(s string) string {
	if !strings.Contains(s, "\n") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] == "" {
			continue
		}
		lines[i] = diagIndent + lines[i]
	}
	return strings.Join(lines, "\n")
}
