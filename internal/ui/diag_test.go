package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// The label column is the whole point of the vocabulary: message text must
// start at the same column for every level, so a WARN and an ERROR stacked in a
// terminal line up and the eye can tell them apart at a glance (#211). Assert
// the rendered bytes, not the constant, so a change to the glyph or the level
// word that breaks alignment fails here.
func TestDiagLabelsShareOneMessageColumn(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, &buf, ColorNever)

	tests := []struct {
		level Level
		want  string
	}{
		{LevelDebug, "• DEBUG  msg"},
		{LevelInfo, "ℹ INFO   msg"},
		{LevelWarn, "⚠ WARN   msg"},
		{LevelError, "✗ ERROR  msg"},
	}
	for _, tc := range tests {
		t.Run(tc.level.String(), func(t *testing.T) {
			buf.Reset()
			p.Diagf(tc.level, "msg")
			if got := buf.String(); got != tc.want+"\n" {
				t.Fatalf("Diagf(%v) = %q, want %q", tc.level, got, tc.want+"\n")
			}
			// The message must begin exactly where diagIndent ends, so a
			// Detailf continuation hangs under it. Count RUNES, not bytes:
			// the glyphs are multi-byte and the terminal column is a rune
			// count (each glyph in the vocabulary is one display cell).
			line := strings.TrimSuffix(buf.String(), "\n")
			col := len([]rune(line)) - len([]rune("msg"))
			if col != len([]rune(diagIndent)) {
				t.Fatalf("%v message starts at column %d, want %d (diagIndent width)",
					tc.level, col, len([]rune(diagIndent)))
			}
		})
	}
}

// A continuation line written with Detailf must land in the message column, and
// an embedded newline inside a single Diagf message must be indented the same
// way — otherwise a multi-line diagnostic breaks apart visually into a labeled
// line plus orphaned text at column 0.
func TestDiagContinuationAlignment(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, &buf, ColorNever)

	p.Warnf("first line\nsecond line")
	p.Detailf("third line")

	want := "⚠ WARN   first line\n" +
		diagIndent + "second line\n" +
		diagIndent + "third line\n"
	if got := buf.String(); got != want {
		t.Fatalf("continuation alignment:\ngot:  %q\nwant: %q", got, want)
	}
}

// A blank line inside a multi-line message stays blank rather than becoming a
// run of trailing whitespace — trailing spaces are invisible noise that shows
// up in diffs and `cat -A`.
func TestDiagBlankContinuationLineNotPadded(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, &buf, ColorNever).Infof("a\n\nb")
	if got := buf.String(); got != "ℹ INFO   a\n\n"+diagIndent+"b\n" {
		t.Fatalf("blank continuation line was padded: %q", got)
	}
}

// Diagnostics go to Err, success goes to Out. That split is what keeps a
// `--json` payload on stdout parseable while warnings still reach the user.
func TestDiagnosticsToErrSuccessToOut(t *testing.T) {
	var out, errb bytes.Buffer
	p := New(&out, &errb, ColorNever)

	p.Errorf("boom")
	p.Warnf("careful")
	p.Infof("fyi")
	if out.Len() != 0 {
		t.Fatalf("diagnostics leaked onto stdout: %q", out.String())
	}
	if n := strings.Count(errb.String(), "\n"); n != 3 {
		t.Fatalf("want 3 diagnostic lines on stderr, got %d: %q", n, errb.String())
	}

	errb.Reset()
	p.Successf(EmojiApplied, "applied: %d ops", 12)
	if errb.Len() != 0 {
		t.Fatalf("success line leaked onto stderr: %q", errb.String())
	}
	if got, want := out.String(), EmojiApplied+" applied: 12 ops\n"; got != want {
		t.Fatalf("Successf = %q, want %q", got, want)
	}
}

// Success lines carry NO level word — that is the explicit product decision
// (an "INFO" on "added agent: claude" is noise). Guard it: a well-meaning
// future edit that routes success through Diagf would silently undo it.
func TestSuccessCarriesNoLevelWord(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, &buf, ColorNever)
	for _, emoji := range []string{EmojiSuccess, EmojiApplied, EmojiRemoved, EmojiImported, EmojiReverted, EmojiInit} {
		buf.Reset()
		p.Successf(emoji, "did the thing")
		got := buf.String()
		for _, word := range []string{"INFO", "WARN", "ERROR", "DEBUG"} {
			if strings.Contains(got, word) {
				t.Fatalf("success line %q must not carry the %s label", got, word)
			}
		}
		if !strings.HasPrefix(got, emoji+" ") {
			t.Fatalf("success line %q must lead with its emoji", got)
		}
	}
}

// Every success emoji must be a single default-emoji-presentation rune. A
// variation-selector form (e.g. "↩️") renders at an inconsistent width
// across terminals, which is exactly why the vocabulary avoids them — and the
// mistake is invisible in source review.
func TestSuccessEmojiHaveNoVariationSelector(t *testing.T) {
	for _, emoji := range []string{EmojiSuccess, EmojiApplied, EmojiRemoved, EmojiImported, EmojiReverted, EmojiInit} {
		if strings.ContainsRune(emoji, '\ufe0f') || strings.ContainsRune(emoji, '\ufe0e') {
			t.Errorf("emoji %q carries a variation selector; pick a default-presentation rune instead", emoji)
		}
		// The doc says "a single default-emoji-presentation rune". Assert the
		// single-rune half too: a ZWJ sequence or a keycap carries no variation
		// selector yet still renders at an unpredictable width.
		if n := len([]rune(emoji)); n != 1 {
			t.Errorf("emoji %q is %d runes; the vocabulary is single-rune by contract", emoji, n)
		}
	}
}

// With color on, the label is wrapped in ANSI but the PLAIN text is unchanged —
// padding is computed before styling, so escape bytes never enter the width
// calculation and a colored terminal aligns exactly like a piped one.
func TestDiagColorDoesNotDisturbAlignment(t *testing.T) {
	var plain, colored bytes.Buffer
	New(&plain, &plain, ColorNever).Warnf("msg")
	New(&colored, &colored, ColorAlways).Warnf("msg")

	if !strings.Contains(colored.String(), codeYellow) {
		t.Fatalf("colored warning is missing its color: %q", colored.String())
	}
	if stripped := stripANSI(colored.String()); stripped != plain.String() {
		t.Fatalf("colored line differs once ANSI is stripped:\ngot:  %q\nwant: %q", stripped, plain.String())
	}
}

// stripANSI removes SGR sequences so a colored line can be compared against its
// plain counterpart. Deliberately minimal — it only needs to handle the
// "\x1b[<params>m" forms this package emits.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// The package doc and docs/architecture.md §11 both claim that a library-side
// slog.Warn, an adapter's "warning: " sentinel through a WarnWriter, and a
// command's own p.Warnf are indistinguishable. That is the whole premise of the
// change — "one vocabulary" — and it was previously only IMPLIED by three
// separate hardcoded expectations in three separate tests, any one of which
// could drift without the others noticing.
//
// Assert the triple directly, for one message, byte-for-byte.
func TestWarnPathsAreByteIdentical(t *testing.T) {
	const msg = "plugin component frontmatter is not strict YAML"

	// Path 1: a command's own p.Warnf.
	var direct bytes.Buffer
	New(&direct, &direct, ColorNever).Warnf("%s", msg)

	// Path 2: the emitter-side sentinel through a WarnWriter (adapter / capture,
	// which cannot import this package).
	var sentinel bytes.Buffer
	sp := New(&sentinel, &sentinel, ColorNever)
	ww := NewWarnWriter(&sentinel, sp)
	fmt.Fprintf(ww, "warning: %s\n", msg)

	// Path 3: a library slog.Warn through the installed handler.
	var viaSlog bytes.Buffer
	lp := New(&viaSlog, &viaSlog, ColorNever)
	_ = slog.New(NewSlogHandler(&viaSlog, lp, slog.LevelInfo)).Handler().
		Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelWarn, msg, 0))

	if direct.String() != sentinel.String() {
		t.Errorf("p.Warnf and the \"warning: \" sentinel diverge:\n  Warnf:    %q\n  sentinel: %q",
			direct.String(), sentinel.String())
	}
	if direct.String() != viaSlog.String() {
		t.Errorf("p.Warnf and slog.Warn diverge:\n  Warnf: %q\n  slog:  %q",
			direct.String(), viaSlog.String())
	}
}

// splitStreamPrinter builds a Printer whose two streams have DIVERGENT color
// decisions, which no other test in this package does — `New(&buf, &buf, …)`
// makes both agree, so it cannot see a per-stream bug at all.
//
// Constructed as a struct literal because that divergence is exactly what New
// cannot produce from two in-memory buffers: `auto` resolves both to false, and
// always/never force both the same way. Only a real terminal-plus-redirect gets
// here, which is the case that shipped the bug.
func splitStreamPrinter(out, errw io.Writer) *Printer {
	return &Printer{Out: out, Err: errw, color: true, colorErr: false, mode: ColorAuto}
}

// The bug this pins: color was resolved once from Out and reused for Err, so
// `agentsync apply 2>err.log` from a terminal wrote ANSI into the file — the
// package doc promises the exact opposite ("color never leaks into a redirect").
// Deleting `colorErr` from New used to break nothing.
func TestDiagnosticColorFollowsTheErrStream(t *testing.T) {
	var out, errb bytes.Buffer
	p := splitStreamPrinter(&out, &errb)

	// Err is the redirected stream (colorErr=false): a diagnostic must be plain
	// even though Out is a color-capable terminal.
	p.Warnf("careful")
	if strings.Contains(errb.String(), "\x1b[") {
		t.Fatalf("ANSI leaked into the redirected Err stream: %q", errb.String())
	}
	if !strings.Contains(errb.String(), "⚠ WARN") {
		t.Fatalf("the label itself was lost: %q", errb.String())
	}

	// A RESULT line on the color-capable Out stream must still be colored.
	p.Successf(EmojiApplied, "applied: 3 ops")
	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("Out is color-capable but the success line is plain: %q", out.String())
	}
}

// Fdiagf takes an explicit writer, so it must style for THAT writer rather than
// inheriting whichever decision the Printer was built around.
func TestFdiagfStylesForItsTargetStream(t *testing.T) {
	var out, errb bytes.Buffer
	p := splitStreamPrinter(&out, &errb)

	p.Fdiagf(&out, LevelError, "to stdout")
	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("a diagnostic routed to the color-capable Out lost its color: %q", out.String())
	}
	p.Fdiagf(&errb, LevelError, "to stderr")
	if strings.Contains(errb.String(), "\x1b[") {
		t.Fatalf("a diagnostic on the redirected Err gained color: %q", errb.String())
	}
}

// An UNRECOGNIZED writer takes the stream matching its output KIND: a diagnostic
// falls back to the Err decision, a result to the Out decision. The two fallbacks
// are deliberately opposite; assert both so a future refactor cannot quietly
// collapse them into one.
func TestUnknownWriterFallbacksAreOpposite(t *testing.T) {
	var out, errb, other bytes.Buffer
	p := splitStreamPrinter(&out, &errb)

	p.Fdiagf(&other, LevelWarn, "diag to a third writer")
	if strings.Contains(other.String(), "\x1b[") {
		t.Fatalf("diagnostic fallback should take the Err (plain) decision: %q", other.String())
	}

	other.Reset()
	p.Fsuccessf(&other, EmojiSuccess, "result to a third writer")
	if !strings.Contains(other.String(), "\x1b[") {
		t.Fatalf("result fallback should take the Out (colored) decision: %q", other.String())
	}
}

// WarnWriter is constructed over p.Err by both its call sites, so it must take the
// Err decision too. It previously styled from p directly — meaning `agentsync
// import 2>err.log` still wrote ANSI into the file, and this path diverged from
// p.Warnf, falsifying the "one vocabulary" claim in the very place the vocabulary
// is supposed to converge.
func TestWarnWriterStylesForItsOwnStream(t *testing.T) {
	var out, errb bytes.Buffer
	p := splitStreamPrinter(&out, &errb)
	w := NewWarnWriter(&errb, p)

	fmt.Fprintf(w, "warning: %s\n", "from an adapter")

	if strings.Contains(errb.String(), "\x1b[") {
		t.Fatalf("WarnWriter leaked ANSI into the redirected Err stream: %q", errb.String())
	}

	// And it must still be byte-identical to p.Warnf on the SAME stream — the
	// divergent-stream case the ColorNever triple test cannot reach.
	var direct bytes.Buffer
	dp := splitStreamPrinter(&out, &direct)
	dp.Warnf("%s", "from an adapter")
	if direct.String() != errb.String() {
		t.Fatalf("WarnWriter and p.Warnf diverge on a split-stream printer:\n  Warnf:      %q\n  WarnWriter: %q",
			direct.String(), errb.String())
	}
}

// Flush must terminate the fragment it emits. An unterminated line leaves the
// next writer to that stream — main's terminal ✗ ERROR line, typically — on the
// same row. This was briefly a separate EndLine() the caller had to pair with
// Flush, and one of the two call sites promptly forgot to.
func TestWarnWriterFlushTerminatesItsLine(t *testing.T) {
	var dest bytes.Buffer
	p := New(&dest, &dest, ColorNever)
	w := NewWarnWriter(&dest, p)

	_, _ = w.Write([]byte("warning: partial with no newline"))
	w.Flush()
	if got := dest.String(); !strings.HasSuffix(got, "\n") {
		t.Fatalf("Flush left an unterminated line: %q", got)
	}

	// Flush on an empty buffer adds nothing — no spurious blank line.
	before := dest.Len()
	w.Flush()
	if dest.Len() != before {
		t.Fatalf("Flush on an empty buffer emitted %q", dest.String()[before:])
	}

	// An already-terminated line is not double-terminated.
	dest.Reset()
	_, _ = w.Write([]byte("warning: complete\n"))
	w.Flush()
	if strings.HasSuffix(dest.String(), "\n\n") {
		t.Fatalf("a terminated line was given a second newline: %q", dest.String())
	}
}

// Level's out-of-range handling: word() must render SOMETHING (the least-severe
// label), but String() must not claim that value is DEBUG.
func TestLevelOutOfRangeIsNamedHonestly(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, &buf, ColorNever)

	rogue := Level(99)
	if got := rogue.String(); got != "Level(99)" {
		t.Fatalf("Level(99).String() = %q, want %q", got, "Level(99)")
	}
	if got := Level(-1).String(); got != "Level(-1)" {
		t.Fatalf("Level(-1).String() = %q", got)
	}
	// It still renders rather than panicking or emitting an empty label.
	p.Diagf(rogue, "msg")
	if !strings.Contains(buf.String(), "DEBUG") {
		t.Fatalf("an out-of-range level should still render the least-severe label: %q", buf.String())
	}
	// And the four real levels keep naming themselves.
	for _, l := range []Level{LevelDebug, LevelInfo, LevelWarn, LevelError} {
		if strings.HasPrefix(l.String(), "Level(") {
			t.Errorf("%d is a defined level but String() reports %q", int(l), l.String())
		}
	}
}

// fdProbe is a writer that reports an Fd (so isTerminal's type assertion
// succeeds) and records that it was asked. The fd is deliberately invalid, so
// term.IsTerminal says "not a terminal" — what is being observed is WHICH streams
// New probes, not the answer.
type fdProbe struct {
	io.Writer
	probed *int
}

func (f fdProbe) Fd() uintptr {
	*f.probed++
	// ^uintptr(0) → int(-1): never a valid descriptor, so IsTerminal is false
	// without touching any real file.
	return ^uintptr(0)
}

// New must resolve the color decision ONCE PER STREAM.
//
// This is the assertion that was missing: the split-stream tests above build a
// Printer as a struct literal, because two in-memory buffers cannot produce
// divergent `auto` decisions (isTerminal is false for both). So they verify that a
// divergence is HONORED, but not that New ever computes one — and with only those,
// changing `colorErr: resolveColor(err, mode)` to resolve from `out` again broke
// nothing. Verified by making that exact edit.
//
// Observing the probe count is the way in: one probe per stream means each stream's
// TTY-ness was asked about independently.
func TestNewResolvesColorPerStream(t *testing.T) {
	// NO_COLOR disables color for ANY value, including empty, and it short-circuits
	// before the isTerminal probe this test observes — so it must be genuinely
	// ABSENT, not empty. t.Setenv registers the restore; Unsetenv does the removal.
	t.Setenv("NO_COLOR", "")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}

	var outProbes, errProbes int
	out := fdProbe{Writer: &bytes.Buffer{}, probed: &outProbes}
	errw := fdProbe{Writer: &bytes.Buffer{}, probed: &errProbes}

	_ = New(out, errw, ColorAuto)

	if outProbes != 1 {
		t.Errorf("Out was probed %d times, want exactly 1", outProbes)
	}
	if errProbes != 1 {
		t.Errorf("Err was probed %d times, want exactly 1 — a single decision reused "+
			"across both streams is the bug this guards", errProbes)
	}
}

// The forced modes must NOT probe either stream: --color=always/never is the
// user's explicit override, and consulting the terminal would be wrong (and, for
// a closed descriptor, potentially a syscall on a stale fd).
func TestForcedColorModesDoNotProbeTheStreams(t *testing.T) {
	for _, mode := range []ColorMode{ColorAlways, ColorNever} {
		var outProbes, errProbes int
		out := fdProbe{Writer: &bytes.Buffer{}, probed: &outProbes}
		errw := fdProbe{Writer: &bytes.Buffer{}, probed: &errProbes}

		p := New(out, errw, mode)
		if outProbes != 0 || errProbes != 0 {
			t.Errorf("mode %v probed the streams (out=%d err=%d); a forced mode must not", mode, outProbes, errProbes)
		}
		// And both streams agree, since the override applies to everything.
		if p.color != p.colorErr {
			t.Errorf("mode %v produced divergent decisions (out=%v err=%v)", mode, p.color, p.colorErr)
		}
	}
}
