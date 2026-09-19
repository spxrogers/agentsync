package testenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// These are guard tests in the TestNewSecretFieldGuard tradition: they read the
// repository's own sources and configuration and fail when a doc claim about
// them stops being true. They live in package testenv (not testenv_test) so the
// unexported variable lists need no exported accessors. Reads only; no FS writes.

// TestAgentHomeVarsReadOnlyThroughPaths enforces the doc claim on
// paths.AgentHomeOverride — "production code reads such variables ONLY through
// this helper, never a raw os.Getenv" — by parsing every non-test Go source in
// the module outside internal/paths and failing on an os.Getenv / os.LookupEnv
// call whose argument is one of the agent-home variables. A raw read would
// bypass the AGENTSYNC_TARGET_ROOT sandbox and re-open the class of leak that
// #270 fixed.
//
// Known residual, deliberately accepted: the check matches only a direct call
// with a string-literal argument, so `os.Getenv(someConst)` and a stored func
// value (`var lookup = os.LookupEnv; lookup("X")`, as internal/secrets does for
// its own knob) evade it. Every raw env read in this repo is a literal or a
// package const named *Env for the harness's OWN knobs; a new agent-home read
// would be reviewed against testenv.agentHomeVars regardless.
func TestAgentHomeVarsReadOnlyThroughPaths(t *testing.T) {
	offenders, _, parsed := scanEnvReads(t)
	// Vacuity guard: the walk must actually have parsed the production tree.
	if parsed < 50 {
		t.Fatalf("parsed only %d production files — the walk's filters are skipping the tree", parsed)
	}
	if len(offenders) != 0 {
		t.Fatalf("agent home variables must be read through paths.AgentHomeOverride, never raw:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// TestEnvReadingPackagesScrubAmbient enforces the scrub's reach: any production
// package that reads the environment raw (any os.Getenv / os.LookupEnv call)
// must have a _test.go file importing internal/testenv, or its default test
// binary runs without the ambient scrub and can depend on the developer's shell.
//
// The check is a per-directory proxy for the real invariant, "every test binary
// imports testenv": it ignores build constraints (a `//go:build live`-only
// import would satisfy it while the default binary stays unscrubbed — not the
// case today; internal/marketplace's default main_test.go is the importer) and
// it does not see packages that only reach an env read through a dependency
// (those are scrubbed if the binary imports testenv, which the FS-touching
// guards make true for every such package here). A dir with no tests has no
// binary to scrub and is exempt (test/bdd/support is compiled only into the bdd
// binary, whose TestMain imports testenv).
func TestEnvReadingPackagesScrubAmbient(t *testing.T) {
	_, envReaders, _ := scanEnvReads(t)
	root := moduleRoot(t)
	for dir := range envReaders {
		hasTests, imports := dirTestsImportTestenv(t, filepath.Join(root, filepath.FromSlash(dir)))
		if hasTests && !imports {
			t.Errorf("%s reads the environment raw but none of its _test.go files import internal/testenv, so its tests run unscrubbed (add `import _ \"github.com/spxrogers/agentsync/internal/testenv\"` or a guard call)", dir)
		}
	}
}

// scanEnvReads parses every non-test Go source outside internal/paths and
// internal/testenv and returns the raw agent-home reads (offenders), the set of
// package dirs with any raw env read, and the number of files parsed.
func scanEnvReads(t *testing.T) (offenders []string, envReaders map[string]bool, parsed int) {
	t.Helper()
	root := moduleRoot(t)
	envReaders = map[string]bool{} // package dir (module-relative) → reads env raw
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// Skip only TOP-LEVEL non-Go trees, so a production package that
			// happens to be named e.g. "bin" deeper down is still scanned.
			if !strings.Contains(rel, "/") {
				switch d.Name() {
				case ".git", "node_modules", "website", "dist", "bin":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasPrefix(rel, "internal/paths/") || strings.HasPrefix(rel, "internal/testenv/") {
			return nil // the legitimate readers: the resolver, and this harness
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return perr
		}
		parsed++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv") {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
				return true
			}
			envReaders[filepath.Dir(rel)] = true
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, _ := strconv.Unquote(lit.Value)
			for _, v := range agentHomeVars {
				if name == v {
					offenders = append(offenders, rel+": os."+sel.Sel.Name+"("+lit.Value+")")
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return offenders, envReaders, parsed
}

// dirTestsImportTestenv reports whether dir has any _test.go files and whether
// one of them imports this package (directly; build constraints are not
// evaluated). imports is meaningful only when hasTests is true.
func dirTestsImportTestenv(t *testing.T, dir string) (hasTests, imports bool) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		hasTests = true
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if importsTestenv(t, e.Name(), src) {
			return true, true
		}
	}
	return hasTests, false
}

// importsTestenv reports whether a Go source imports this package. Split from
// the directory walk so the negative case can be unit-tested from a fixture
// string with no filesystem (this package runs in the host test-fast set).
func importsTestenv(t *testing.T, name string, src []byte) bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) == "github.com/spxrogers/agentsync/internal/testenv" {
			return true
		}
	}
	return false
}

// TestImportsTestenv makes the env-reading-packages rule load-bearing on its
// own: every production package complies today, so without this the detector
// could rot to "always true" unnoticed.
func TestImportsTestenv(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{name: "direct import", src: "package x_test\nimport \"github.com/spxrogers/agentsync/internal/testenv\"\nvar _ = testenv.EnvVar\n", want: true},
		{name: "blank import", src: "package x\nimport _ \"github.com/spxrogers/agentsync/internal/testenv\"\n", want: true},
		{name: "no import", src: "package x_test\nimport \"testing\"\nfunc TestX(*testing.T) {}\n", want: false},
		{name: "lookalike path", src: "package x\nimport _ \"github.com/other/agentsync/internal/testenv\"\n", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := importsTestenv(t, "x_test.go", []byte(tc.src)); got != tc.want {
				t.Fatalf("importsTestenv = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConfiguredEnvLegCoversAmbientVars is the parity guard for the three
// places that name the ambient variables: this package's list (the scrub), the
// container runner's configured leg (scripts/test-in-container.sh), and the
// host test-fast configured leg (.github/workflows/ci.yml). A variable the
// scrub knows about but the CI legs never export is a variable whose leak CI
// can never catch — the same dual-list drift class the goreleaser pin guard
// closes. The ci.yml match is anchored to a YAML mapping key at any indent
// (`^\s+NAME:\s`) rather than a fixed indentation, so re-indenting the workflow
// does not break it while a removed key still does.
func TestConfiguredEnvLegCoversAmbientVars(t *testing.T) {
	root := moduleRoot(t)
	script := readFile(t, filepath.Join(root, "scripts", "test-in-container.sh"))
	ci := readFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	// Only the test-fast job's text counts, so a key relocated to another job
	// cannot satisfy the check.
	start, end := strings.Index(ci, "\n  test-fast:"), strings.Index(ci, "\n  test-release:")
	if start < 0 || end < 0 || end < start {
		t.Fatalf("ci.yml: could not locate the test-fast job block (start=%d end=%d)", start, end)
	}
	testFast := ci[start:end]
	want := append([]string{"AGENTSYNC_HOME"}, ambientVars...)
	for _, v := range want {
		if !strings.Contains(script, `-e "`+v+`=`) {
			t.Errorf("scripts/test-in-container.sh configured leg does not export %s", v)
		}
		if !regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(v) + `:\s`).MatchString(testFast) {
			t.Errorf(".github/workflows/ci.yml test-fast configured step does not export %s", v)
		}
	}
	// And the configured recipe really is what CI runs and what the script keys on.
	just := readFile(t, filepath.Join(root, "justfile"))
	if !strings.Contains(just, "AGENTSYNC_TEST_CONFIGURED_ENV=1 ./scripts/test-in-container.sh") {
		t.Error("justfile: test-release-configured must set AGENTSYNC_TEST_CONFIGURED_ENV=1 for the runner script")
	}
	if !strings.Contains(ci, "recipe: [test-release, test-release-configured]") {
		t.Error("ci.yml: the test-release matrix must run the test-release-configured recipe")
	}
	// The flag must cross INTO the container — the entrypoint's fake-PATH block
	// keys off it there (round 6 of the #271 review found it read only on the
	// host, leaving that block dead in every leg) — and it must cross ONLY on
	// the configured leg: the forward has to sit inside the runner's
	// `if [[ … == "1" ]]` block, or the pristine leg stops being pristine and
	// nothing else would notice (round 7). So the asserts below are scoped to
	// that block, not the whole file.
	configuredBlock := shellIfBlock(t, script, `if [[ "${AGENTSYNC_TEST_CONFIGURED_ENV:-}" == "1" ]]`)
	for _, v := range append([]string{"AGENTSYNC_TEST_CONFIGURED_ENV=1", "AGENTSYNC_HOME="}, ambientVars...) {
		if !strings.Contains(configuredBlock, `-e "`+v) {
			t.Errorf("scripts/test-in-container.sh: `-e %q…` must be inside the configured-leg if-block, and only there", v)
		}
	}
	// Outside that block no `-e` may carry a configured-env variable (prose
	// mentions in comments are fine; only the `-e "NAME=` form reaches the container).
	pristine := strings.Replace(script, configuredBlock, "", 1)
	for _, v := range append([]string{"AGENTSYNC_TEST_CONFIGURED_ENV", "AGENTSYNC_HOME"}, ambientVars...) {
		if strings.Contains(pristine, `-e "`+v+`=`) {
			t.Errorf("scripts/test-in-container.sh: `-e %q=…` outside the configured-leg if-block would make the pristine leg configured too", v)
		}
	}
	entrypoint := readFile(t, filepath.Join(root, "test", "container", "entrypoint.sh"))
	entryBlock := shellIfBlock(t, entrypoint, `if [[ "${AGENTSYNC_TEST_CONFIGURED_ENV:-}" == "1" ]]`)
	for _, want := range []string{`command -v codex`, `export AGENTSYNC_TEST_AMBIENT_BIN=`} {
		if !strings.Contains(entryBlock, want) {
			t.Errorf("test/container/entrypoint.sh configured block must contain %q (runtime self-check / Go-side marker)", want)
		}
	}
}

// shellIfBlock returns the text from the first line containing header through
// the matching top-level `fi` (the first line that is exactly "fi"), so guards
// can assert what is and is not inside a shell if-block.
func shellIfBlock(t *testing.T, src, header string) string {
	t.Helper()
	start := strings.Index(src, header)
	if start < 0 {
		t.Fatalf("shell source has no %q block", header)
	}
	end := strings.Index(src[start:], "\nfi\n")
	if end < 0 {
		t.Fatalf("shell source: no closing `fi` for %q", header)
	}
	return src[start : start+end+len("\nfi\n")]
}

// TestConfiguredLegIsLive is the behavioural counterpart of the text guards
// above: when the container entrypoint has installed the fake agent binaries it
// exports AGENTSYNC_TEST_AMBIENT_BIN, and from inside a test exec.LookPath must
// then resolve an agent binary INTO that directory. It is keyed on the marker,
// not on AGENTSYNC_TEST_CONFIGURED_ENV, because the runner's `shell` / `--`
// modes forward the flag but bypass the entrypoint. It deliberately does NOT
// assert the opposite on a pristine run — "no codex on PATH" would re-import
// exactly the host dependence #270 is about (the host test-fast legs may well
// have a real agent installed). Pure-unit: no filesystem writes.
func TestConfiguredLegIsLive(t *testing.T) {
	dir, ok := os.LookupEnv("AGENTSYNC_TEST_AMBIENT_BIN")
	if !ok {
		t.Skip("not the configured container leg (AGENTSYNC_TEST_AMBIENT_BIN unset)")
	}
	for _, bin := range []string{"codex", "claude", "grok"} {
		got, err := exec.LookPath(bin)
		if err != nil {
			t.Fatalf("configured leg claims stubs in %s but LookPath(%q) failed: %v", dir, bin, err)
		}
		if filepath.Dir(got) != filepath.Clean(dir) {
			t.Fatalf("LookPath(%q) = %s; want the stub in %s — the fake-PATH leg is not what PATH resolves", bin, got, dir)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}
	return root
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
