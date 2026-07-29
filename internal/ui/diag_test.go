package ui

import (
	"bytes"
	"strings"
	"testing"
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
			// The message must begin exactly where DiagIndent ends, so a
			// Detailf continuation hangs under it. Count RUNES, not bytes:
			// the glyphs are multi-byte and the terminal column is a rune
			// count (each glyph in the vocabulary is one display cell).
			line := strings.TrimSuffix(buf.String(), "\n")
			col := len([]rune(line)) - len([]rune("msg"))
			if col != len([]rune(DiagIndent)) {
				t.Fatalf("%v message starts at column %d, want %d (DiagIndent width)",
					tc.level, col, len([]rune(DiagIndent)))
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
		DiagIndent + "second line\n" +
		DiagIndent + "third line\n"
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
	if got := buf.String(); got != "ℹ INFO   a\n\n"+DiagIndent+"b\n" {
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
		if strings.ContainsRune(emoji, '️') || strings.ContainsRune(emoji, '︎') {
			t.Errorf("emoji %q carries a variation selector; pick a default-presentation rune instead", emoji)
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
