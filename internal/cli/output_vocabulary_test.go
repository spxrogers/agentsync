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
	"strconv"
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

// sweptDirs are the source directories the ad-hoc-prefix sweep covers, relative
// to the repo root.
//
// `cmd/agentsync` is in the list because that is where the #211 regression
// ACTUALLY lived: the offending line was `fmt.Fprintln(os.Stderr, "agentsync:",
// err)` in main(), not in internal/cli. A sweep scoped to this package alone
// passed green with that exact line restored — verified by reintroducing it.
var sweptDirs = []string{"internal/cli", "cmd/agentsync"}

// repoRoot locates the repository root from this test file's compiled-in path.
//
// It CANNOT be derived from the working directory: this package's TestMain chdirs
// into a scratch dir so filesystem-touching tests never run against the repo, so
// `filepath.Glob("*.go")` here matches nothing and any sweep built on it passes
// vacuously — which is exactly how this guard was first written. runtime.Caller
// pins the path at compile time instead.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate this test file")
	}
	// <root>/internal/cli/output_vocabulary_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(self)))
}

// bannedPrefixes are the diagnostic prefixes this codebase used to hand-roll,
// each of which produced a notice that looked different from every other one.
// They are now all expressed through the ui vocabulary.
//
// Two matching modes, because the three prefixes are not alike:
//
//   - `labelOnly: false` (agentsync:, note:) — these are never legitimate in
//     output, in ANY form. Matched as a PREFIX of the unquoted literal, because
//     the original #211 emitter and its likely reintroductions are
//     `"agentsync:"`, `"agentsync: %v\n"`, and `"agentsync: "` — an equality
//     check catches only the first, which is how the first version of this guard
//     passed with the regression restored.
//   - `labelOnly: true` (warning:) — the bare `warning: ` sentinel HEADING A
//     FULL MESSAGE is a deliberate contract: the adapter and capture packages
//     must not import ui, so they emit that plain prefix into an io.Writer and
//     ui.WarnWriter rewrites it into the WARN label. Those must stay legal. What
//     must not is `warning:` used as a standalone styled label token, e.g. the
//     old `p.Yellow("warning:")`. So ban only the bare-token forms.
//
// Deliberately narrow in WHICH prefixes are listed: a broader set (e.g. "scope:",
// "ok:") would also flag legitimate field labels inside a report body, where no
// level belongs — `explain` prints `  scope: user` as a data row, not a notice.
type bannedPrefix struct {
	prefix    string
	labelOnly bool
	instead   string
}

// banned reports whether the unquoted literal val trips this rule.
func (b bannedPrefix) banned(val string) bool {
	if b.labelOnly {
		return val == b.prefix || val == b.prefix+" "
	}
	return strings.HasPrefix(val, b.prefix)
}

var bannedPrefixes = []bannedPrefix{
	{prefix: "agentsync:", instead: "p.Warnf / p.Errorf — the level label replaces the program prefix"},
	{prefix: "note:", instead: "p.Infof"},
	{prefix: "warning:", labelOnly: true, instead: "p.Warnf, or the \"warning: <message>\" sentinel into a ui.WarnWriter"},
}

// exemptLiterals are specific literals that match a banned prefix but are not
// terminal diagnostics. Keyed by the unquoted literal so adding one is an
// explicit, reviewable decision rather than a loosened pattern.
var exemptLiterals = map[string]string{
	// A panic value, not a printed diagnostic: it names the program because it
	// surfaces through Go's own panic output, which carries no agentsync framing.
	// It can never reach ReportError (a panic does not return an error), so it
	// cannot produce a doubled "✗ ERROR  agentsync: …" line.
	"agentsync: adapter registry wiring bug: %w": "internal panic value, never a printed diagnostic",
}

// TestNoAdHocDiagnosticPrefixes is the regression fence for #211's follow-up:
// the whole value of the vocabulary is that EVERY diagnostic looks the same, and
// that property dies the moment one new command hand-rolls its own prefix. A
// hand-rolled prefix compiles, passes review as "just a Fprintf", and breaks
// nothing — exactly the failure class that needs a mechanical guard.
//
// Scoped to string LITERALS in non-test sources, so a comment discussing the old
// format (several do, for history) never trips it.
func TestNoAdHocDiagnosticPrefixes(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	swept := 0
	for _, dir := range sweptDirs {
		files, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no sources found under %s — the sweep would pass vacuously", dir)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			swept++
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
				// Unquote so both interpreted ("…") and raw (`…`) literals are
				// compared as the text the terminal would actually receive.
				val, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					return true
				}
				if why, ok := exemptLiterals[val]; ok {
					_ = why // documented exemption; see exemptLiterals
					return true
				}
				for _, b := range bannedPrefixes {
					if b.banned(val) {
						t.Errorf("%s: ad-hoc diagnostic prefix %q — use %s instead",
							fset.Position(lit.Pos()), val, b.instead)
					}
				}
				return true
			})
		}
	}
	// A sweep that silently stops finding files is the failure mode this whole
	// test exists to avoid, twice over now. Pin a floor.
	if swept < 25 {
		t.Fatalf("swept only %d source files; the sweep has lost its targets", swept)
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
