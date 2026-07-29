package cli_test

import (
	"bytes"
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

	"github.com/spxrogers/agentsync/internal/ui"
)

// ---------------------------------------------------------------------------
// The guard: no new ad-hoc diagnostic prefixes
// ---------------------------------------------------------------------------

// sweptDirs are the source directories the ad-hoc-prefix sweep covers, relative
// to the repo root, each with the minimum number of non-test files it must
// contain.
//
// `cmd/agentsync` is here because that is where the #211 regression ACTUALLY
// lived: the offending line was `fmt.Fprintln(os.Stderr, "agentsync:", err)` in
// main(), not in internal/cli. A sweep scoped to this package alone passed green
// with that exact line restored — verified by reintroducing it.
//
// The floors are PER DIRECTORY, and TestSweptDirsCoverTheEmitters pins the set
// itself, because a single aggregate floor does not protect this list. With 31
// files in internal/cli and 1 in cmd/agentsync, deleting the cmd/agentsync entry
// left 31 — comfortably over any aggregate floor — so the round-1 hole could be
// reopened without failing anything. A one-file directory cannot be defended by
// counting everything.
//
// DELIBERATELY OUT OF SCOPE, so a future reader does not read these as misses:
//
//   - `internal/testenv` prints three `agentsync: …` lines to os.Stderr
//     (container.go), but it is a TestMain host-refusal guard that never runs in
//     the shipped binary and exits before any CLI output exists. Naming the
//     program there is right, exactly as it is for a panic value.
//   - `internal/source` defines `agentsync:fragment` and `agentsync:managed` as
//     RESERVED marker tokens written into memory files. They are file content, not
//     terminal output, and a prefix match would flag them — which is the argument
//     for scoping this sweep to the packages that actually emit diagnostics rather
//     than widening it repo-wide.
var sweptDirs = []struct {
	dir      string
	minFiles int
}{
	{dir: "internal/cli", minFiles: 25},
	{dir: "cmd/agentsync", minFiles: 1},
}

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
	// It can never reach reportErrorTo (a panic does not return an error), so it
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
	for _, sd := range sweptDirs {
		files, err := filepath.Glob(filepath.Join(root, sd.dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		var nonTest int
		for _, p := range files {
			if !strings.HasSuffix(p, "_test.go") {
				nonTest++
			}
		}
		if nonTest < sd.minFiles {
			t.Fatalf("swept only %d non-test file(s) under %s, want >= %d — the sweep has lost its targets there",
				nonTest, sd.dir, sd.minFiles)
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
}

// TestSweptDirsCoverTheEmitters pins the sweep's SCOPE, separately from its
// matcher.
//
// The per-directory floors catch a directory going empty; they cannot catch an
// entry being deleted outright, which is precisely how the round-1 hole existed in
// the first place (`cmd/agentsync` absent). Pin the set so removing a directory is
// a test failure and not a silent narrowing.
func TestSweptDirsCoverTheEmitters(t *testing.T) {
	must := map[string]bool{
		// Where the CLI's diagnostics are emitted from. `cmd/agentsync` is
		// non-negotiable: it held the original #211 line.
		"internal/cli":  false,
		"cmd/agentsync": false,
	}
	for _, sd := range sweptDirs {
		if _, ok := must[sd.dir]; ok {
			must[sd.dir] = true
		}
	}
	for dir, covered := range must {
		if !covered {
			t.Errorf("%s dropped out of sweptDirs — that reopens the hole this guard exists to close", dir)
		}
	}
	// A floor of 0 would make the per-directory check vacuous, so an entry could be
	// neutered (rather than removed) without tripping the membership pin above.
	for _, sd := range sweptDirs {
		if sd.minFiles < 1 {
			t.Errorf("sweptDirs entry %q has minFiles=%d; a floor below 1 cannot detect a "+
				"moved or renamed directory", sd.dir, sd.minFiles)
		}
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
