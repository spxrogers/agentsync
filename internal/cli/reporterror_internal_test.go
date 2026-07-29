package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/ui"
)

// resolvedColorMode is written in the root PersistentPreRunE and read by
// ReportError in main(), after cobra has returned. Nothing else observes it, so
// deleting the assignment previously failed no test — verified by removing it and
// running the whole suite green.
//
// These tests close that hole from both ends: the assignment must happen, and it
// must reach ReportError's rendering. An internal test because the variable is
// package-private on purpose.
func TestPersistentPreRunRecordsColorMode(t *testing.T) {
	tests := []struct {
		flag string
		want ui.ColorMode
	}{
		{"--color=never", ui.ColorNever},
		{"--color=always", ui.ColorAlways},
		{"--color=auto", ui.ColorAuto},
	}
	for _, tc := range tests {
		t.Run(tc.flag, func(t *testing.T) {
			// Poison it first, so a test that passes only because the zero value
			// happens to equal `want` cannot pass for the wrong reason.
			resolvedColorMode = ui.ColorMode(-1)
			t.Cleanup(func() { resolvedColorMode = ui.ColorAuto })

			var buf bytes.Buffer
			root := NewRoot()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs([]string{"version", tc.flag})
			if err := root.Execute(); err != nil {
				t.Fatalf("version %s: %v", tc.flag, err)
			}
			if resolvedColorMode != tc.want {
				t.Fatalf("resolvedColorMode = %v after %s, want %v", resolvedColorMode, tc.flag, tc.want)
			}
		})
	}
}

// An invalid --color must NOT skip the slog installation or lose the recorded
// mode: gating that on `err == nil` meant a bad flag value left the process with
// no handler, so library warnings fell back to the stdlib timestamped line —
// reproducing the exact #211 shape in the one case where the user has already
// made a mistake and most needs a legible diagnostic.
func TestPersistentPreRunSurvivesInvalidColor(t *testing.T) {
	resolvedColorMode = ui.ColorMode(-1)
	t.Cleanup(func() { resolvedColorMode = ui.ColorAuto })

	var buf bytes.Buffer
	root := NewRoot()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"version", "--color=banana"})
	// `version` does not build a Printer, so it does not surface the bad value;
	// what matters here is that the pre-run still ran to completion.
	_ = root.Execute()

	if resolvedColorMode != ui.ColorAuto {
		t.Fatalf("an invalid --color must degrade to auto, got %v", resolvedColorMode)
	}
}

// ReportError must honor the mode the pre-run recorded — the end of the chain
// that makes `--color=never` actually reach the terminal error line.
func TestReportErrorHonorsRecordedColorMode(t *testing.T) {
	orig := resolvedColorMode
	t.Cleanup(func() { resolvedColorMode = orig })

	var colored, plain bytes.Buffer
	resolvedColorMode = ui.ColorAlways
	if code := reportErrorToStream(&colored, errors.New("boom")); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	resolvedColorMode = ui.ColorNever
	if code := reportErrorToStream(&plain, errors.New("boom")); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	if !strings.Contains(colored.String(), "\x1b[") {
		t.Fatalf("ColorAlways produced no ANSI: %q", colored.String())
	}
	if strings.Contains(plain.String(), "\x1b[") {
		t.Fatalf("ColorNever leaked ANSI: %q", plain.String())
	}
	if !strings.Contains(plain.String(), "✗ ERROR") {
		t.Fatalf("plain terminal error lost its label: %q", plain.String())
	}
}

// The terminal error is the last thing a failing run prints, and an error chain
// routinely carries third-party agent-config text. A bare \r in it rewinds the
// cursor over the "✗ ERROR" label, letting crafted config forge a success line;
// a \x1b introduces an arbitrary escape sequence.
func TestReportErrorSanitizesTheErrorChain(t *testing.T) {
	var buf bytes.Buffer
	ReportErrorTo(&buf, ui.ColorNever, errors.New("render codex: subagent \"a\rb\x1b]0;pwned\x07\" is bad"))

	got := buf.String()
	for _, bad := range []string{"\r", "\x1b]", "\x07"} {
		if strings.Contains(got, bad) {
			t.Fatalf("control byte %q survived into the terminal error: %q", bad, got)
		}
	}
	if !strings.HasPrefix(got, "✗ ERROR") {
		t.Fatalf("label lost: %q", got)
	}
	if !strings.Contains(got, "render codex:") {
		t.Fatalf("message body was blanked rather than sanitized: %q", got)
	}
}

// A multi-line error keeps its line structure — sanitizing wholesale would
// concatenate the lines (ui.Sanitize STRIPS newlines) and defeat the
// message-column indentation.
func TestReportErrorKeepsMultiLineStructure(t *testing.T) {
	var buf bytes.Buffer
	ReportErrorTo(&buf, ui.ColorNever, errors.New("first line\nsecond line"))
	got := buf.String()
	if !strings.Contains(got, "first line\n") || !strings.Contains(got, "second line") {
		t.Fatalf("multi-line error was collapsed: %q", got)
	}
	if strings.Contains(got, "first linesecond line") {
		t.Fatalf("lines were concatenated with no separator: %q", got)
	}
}

// reportErrorToStream is ReportError with an injectable writer, exercising the
// same resolvedColorMode read that main() relies on.
func reportErrorToStream(w *bytes.Buffer, err error) int {
	return ReportErrorTo(w, resolvedColorMode, err)
}
