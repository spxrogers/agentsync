package cli_test

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/cli"
	"github.com/spxrogers/agentsync/internal/ui"
)

// ---------------------------------------------------------------------------
// The terminal error line (#211)
// ---------------------------------------------------------------------------

// The exact failure from the issue: `agentsync status` exits 1 and the last
// thing printed is the error. It used to be a flat, unlabeled, uncolored
// `agentsync: render codex: …` sitting directly below a WARN — nothing marked
// it as the failure. It must now carry the ERROR label.
func TestReportError_CarriesTheErrorLabel(t *testing.T) {
	var buf bytes.Buffer
	code := cli.ReportErrorTo(&buf, ui.ColorNever, errors.New(
		`render codex: codex subagents "code-reviewer" and "code-reviewer" resolve to the same agent name`,
	))

	if code != 1 {
		t.Fatalf("ReportErrorTo exit code = %d, want 1", code)
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

// With color on, the label is red — the second signal (after the glyph and the
// word) that separates a failure from the warning above it.
func TestReportError_ColorsTheLabel(t *testing.T) {
	var buf bytes.Buffer
	cli.ReportErrorTo(&buf, ui.ColorAlways, errors.New("boom"))
	if !strings.Contains(buf.String(), "\x1b[31m") {
		t.Fatalf("colored terminal error is missing red: %q", buf.String())
	}
}

// The quiet exit-code sentinel (`status --exit-code`, `diff --exit-code`)
// carries its own code and an empty message: it must map to that code and print
// NOTHING, so a CI gate gets a stable non-zero exit with no spurious line.
func TestReportError_QuietSentinelPrintsNothing(t *testing.T) {
	var buf bytes.Buffer
	code := cli.ReportErrorTo(&buf, ui.ColorNever, quietExit(3))
	if code != 3 {
		t.Fatalf("exit code = %d, want the sentinel's 3", code)
	}
	if buf.Len() != 0 {
		t.Fatalf("quiet sentinel must print nothing; got: %q", buf.String())
	}
}

func TestReportError_NilIsZeroAndSilent(t *testing.T) {
	var buf bytes.Buffer
	if code := cli.ReportErrorTo(&buf, ui.ColorNever, nil); code != 0 || buf.Len() != 0 {
		t.Fatalf("nil error: code=%d out=%q, want 0 and empty", code, buf.String())
	}
}

// quietExitErr mirrors the shape of the status/diff sentinel: an error whose
// message is empty and which reports its own process exit code.
type quietExitErr int

func (q quietExitErr) Error() string { return "" }
func (q quietExitErr) ExitCode() int { return int(q) }

func quietExit(code int) error { return quietExitErr(code) }

// The sentinel must be found through a wrapper too — commands wrap errors on
// the way up, and a `%w`-wrapped sentinel that stopped being recognized would
// turn a clean CI gate into a spurious ERROR line.
func TestReportError_QuietSentinelThroughWrapper(t *testing.T) {
	var buf bytes.Buffer
	code := cli.ReportErrorTo(&buf, ui.ColorNever, fmt.Errorf("status: %w", quietExit(2)))
	if code != 2 || buf.Len() != 0 {
		t.Fatalf("wrapped sentinel: code=%d out=%q, want 2 and empty", code, buf.String())
	}
}

// ---------------------------------------------------------------------------
// The guard: no new ad-hoc diagnostic prefixes
// ---------------------------------------------------------------------------

// packageSourceDir returns the directory holding this package's sources.
//
// It CANNOT be derived from the working directory: this package's TestMain
// chdirs into a scratch dir so filesystem-touching tests never run against the
// repo, so a `filepath.Glob("*.go")` here matches nothing and any sweep built on
// it passes vacuously — which is exactly how this guard was first written, and
// exactly the failure a source sweep must never have. runtime.Caller pins the
// path at compile time instead.
func packageSourceDir(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate this test file")
	}
	return filepath.Dir(self)
}

// bannedPrefixes are the diagnostic prefixes this package used to hand-roll,
// each of which produced a notice that looked different from every other one.
// They are now all expressed through the ui vocabulary.
//
// Deliberately narrow — only prefixes whose ONLY plausible use is labeling a
// diagnostic. A broader list (e.g. "scope:", "ok:") would also flag legitimate
// field labels inside a report body, where no level belongs: `explain` prints
// `  scope: user` as a data row, not as a notice about the run.
var bannedPrefixes = []struct {
	literal string
	instead string
}{
	{`"agentsync:"`, "p.Warnf / p.Errorf — the level label replaces the program prefix"},
	{`"warning:"`, "p.Warnf (or the bare \"warning: \" sentinel into a ui.WarnWriter)"},
	{`"note:"`, "p.Infof"},
}

// TestNoAdHocDiagnosticPrefixes is the regression fence for #211's follow-up:
// the whole value of the vocabulary is that EVERY diagnostic looks the same, and
// that property dies the moment one new command hand-rolls its own prefix. A
// hand-rolled prefix compiles, passes review as "just a Fprintf", and breaks
// nothing — exactly the failure class that needs a mechanical guard.
//
// Scoped to string LITERALS in this package's non-test sources, so a comment
// discussing the old format (several do, for history) never trips it.
func TestNoAdHocDiagnosticPrefixes(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(packageSourceDir(t), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no sources found — the sweep would pass vacuously")
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for _, b := range bannedPrefixes {
				// Exact-literal match: `"warning: %s"` inside a WarnWriter-bound
				// Fprintf is the documented emitter contract and must stay legal,
				// while a bare `p.Yellow("warning:")` prefix must not.
				if lit.Value == b.literal {
					t.Errorf("%s: ad-hoc diagnostic prefix %s — use %s instead",
						fset.Position(lit.Pos()), b.literal, b.instead)
				}
			}
			return true
		})
	}
}

// The emitter-side sentinel is a contract between the adapter/capture packages
// (which must not import ui) and ui.WarnWriter. If it is ever renamed on one
// side only, warnings silently stop being labeled — nothing fails to compile.
// Pin both halves here.
func TestWarnSentinelStaysWiredToTheWarnLabel(t *testing.T) {
	var dest bytes.Buffer
	p := ui.New(&dest, &dest, ui.ColorNever)
	w := ui.NewWarnWriter(&dest, p)

	fmt.Fprintf(w, "warning: %s\n", "something happened")

	want := ui.LevelWarn.Label(p) + "  something happened\n"
	if got := dest.String(); got != want {
		t.Fatalf("the \"warning: \" sentinel no longer renders as the WARN label:\ngot:  %q\nwant: %q", got, want)
	}
}
