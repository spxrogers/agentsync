package cli

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	aslog "github.com/spxrogers/agentsync/internal/log"
	"github.com/spxrogers/agentsync/internal/ui"
)

// detachAfter unbinds the process-wide slog default once this test is done.
//
// The cli_test package gets this from runCLI's helper; an INTERNAL test calling
// NewRoot().Execute() directly cannot see that helper, and every such call leaves
// slog.Default() bound to a buffer this test is about to drop.
func detachAfter(t *testing.T) {
	t.Helper()
	t.Cleanup(aslog.Detach)
}

// Execute() reads the resolved --color decision off the root command AFTER cobra
// has parsed and run it, which is what let a package-level `resolvedColorMode`
// var (written in PersistentPreRunE, read from main) be deleted entirely.
//
// This pins that mechanism. It calls colorModeOf on the executed root exactly as
// Execute does; Execute itself is then a thin shim over this plus the
// already-tested reportErrorTo, and writes to the real os.Stderr, which is why it
// is not driven directly here.
func TestColorModeIsReadableFromAnExecutedRoot(t *testing.T) {
	tests := []struct {
		flag string
		want ui.ColorMode
	}{
		{"--color=never", ui.ColorNever},
		{"--color=always", ui.ColorAlways},
		{"--color=auto", ui.ColorAuto},
		// An unparseable value must degrade to auto, NOT error out of the report
		// path: ParseColorMode returns ColorAuto alongside its error, which is what
		// makes Execute's `mode, _ := colorModeOf(root)` safe.
		{"--color=banana", ui.ColorAuto},
	}
	for _, tc := range tests {
		t.Run(tc.flag, func(t *testing.T) {
			detachAfter(t)
			var buf bytes.Buffer
			root := NewRoot()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs([]string{"version", tc.flag})
			_ = root.Execute()

			got, _ := colorModeOf(root)
			if got != tc.want {
				t.Fatalf("colorModeOf(root) = %v after %s, want %v", got, tc.flag, tc.want)
			}
		})
	}
}

// An invalid --color must not prevent the slog handler from being installed.
// Gating installation on newPrinter's error meant a bad value left NO handler, so
// library warnings fell back to the stdlib timestamped line — reproducing the
// exact #211 shape in the one case where the user has already made a mistake and
// most needs a legible diagnostic.
func TestPersistentPreRunInstallsDespiteInvalidColor(t *testing.T) {
	detachAfter(t)
	var buf bytes.Buffer
	root := NewRoot()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"version", "--color=banana"})
	_ = root.Execute()

	// The installed handler writes to the root's stderr. Emitting through the
	// process default must therefore land in our buffer — if the pre-run skipped
	// installation, this goes to the real os.Stderr instead and the buffer is bare.
	before := buf.Len()
	slogWarnForTest("installed-check")
	if !strings.Contains(buf.String()[before:], "installed-check") {
		t.Fatalf("no slog handler installed after an invalid --color; got: %q", buf.String()[before:])
	}
	if !strings.Contains(buf.String()[before:], "WARN") {
		t.Fatalf("the installed handler is not labeling: %q", buf.String()[before:])
	}
}

func TestReportErrorToHonorsColorMode(t *testing.T) {
	var colored, plain bytes.Buffer
	if code := reportErrorTo(&colored, ui.ColorAlways, errors.New("boom")); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if code := reportErrorTo(&plain, ui.ColorNever, errors.New("boom")); code != 1 {
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
// \x1b introduces an arbitrary escape sequence.
func TestReportErrorSanitizesTheErrorChain(t *testing.T) {
	var buf bytes.Buffer
	reportErrorTo(&buf, ui.ColorNever, errors.New("render codex: subagent \"a\rb\x1b]0;pwned\x07\" is bad"))

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
	reportErrorTo(&buf, ui.ColorNever, errors.New("first line\nsecond line"))
	got := buf.String()
	if !strings.Contains(got, "first line\n") || !strings.Contains(got, "second line") {
		t.Fatalf("multi-line error was collapsed: %q", got)
	}
	if strings.Contains(got, "first linesecond line") {
		t.Fatalf("lines were concatenated with no separator: %q", got)
	}
}

// slogWarnForTest emits through the PROCESS DEFAULT logger, which is what the
// pre-run installs. Deliberately not a *slog.Logger built here: the point is to
// observe whatever handler the root command left installed.
func slogWarnForTest(msg string) { slog.Warn(msg) }

// The exact failure from #211: `agentsync status` exits 1 and the last thing
// printed is the error. It used to be a flat, unlabeled, uncolored
// `agentsync: render codex: …` sitting directly below a WARN, with nothing marking
// it as the failure.
func TestReportErrorCarriesTheErrorLabel(t *testing.T) {
	var buf bytes.Buffer
	code := reportErrorTo(&buf, ui.ColorNever, errors.New(
		`render codex: codex subagents "code-reviewer" and "code-reviewer" resolve to the same agent name`,
	))
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	got := buf.String()
	if !strings.HasPrefix(got, "✗ ERROR  ") {
		t.Fatalf("terminal error must lead with the ERROR label; got: %q", got)
	}
	if strings.Contains(got, "agentsync:") {
		t.Fatalf("the redundant program prefix should be gone; got: %q", got)
	}
	if !strings.Contains(got, "resolve to the same agent name") {
		t.Fatalf("the error message body was lost; got: %q", got)
	}
}

// The quiet exit-code sentinel (`status --exit-code`, `diff --exit-code`) carries
// its own code and an empty message: it must map to that code and print NOTHING,
// so a CI gate gets a stable non-zero exit with no spurious diagnostic line. It
// must be found through a `%w` wrapper too, since commands wrap on the way up.
func TestReportErrorQuietSentinel(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"direct", quietExitErr(3), 3},
		{"wrapped", fmt.Errorf("status: %w", quietExitErr(2)), 2},
		{"nil", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if code := reportErrorTo(&buf, ui.ColorNever, tc.err); code != tc.want {
				t.Fatalf("exit code = %d, want %d", code, tc.want)
			}
			if buf.Len() != 0 {
				t.Fatalf("must print nothing; got: %q", buf.String())
			}
		})
	}
}

// quietExitErr mirrors the status/diff sentinel: an empty message plus its own
// process exit code.
type quietExitErr int

func (q quietExitErr) Error() string { return "" }
func (q quietExitErr) ExitCode() int { return int(q) }
