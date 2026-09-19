package testenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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

// TestConfiguredEnvLegCoversAmbientVars is the parity guard for every place
// that names the ambient variables or wires the configured leg: this package's
// list (the scrub), the container runner's configured if-block
// (scripts/test-in-container.sh), the host test-fast configured STEP
// (.github/workflows/ci.yml), the justfile recipe, and the entrypoint's
// configured block (test/container/entrypoint.sh). A variable the scrub knows
// about but the CI legs never export is a variable whose leak CI can never
// catch — the same dual-list drift class the goreleaser pin guard closes — and
// a configured-env variable that reaches the PRISTINE leg quietly destroys the
// two-leg design. Every check is therefore scoped to the block it belongs in
// (shellIfBlock; the ci.yml step slice), never the whole file. The ci.yml key
// match is a YAML mapping key at any indent (`^\s+NAME:\s`) so re-indenting the
// workflow does not break it while a removed key still does.
func TestConfiguredEnvLegCoversAmbientVars(t *testing.T) {
	root := moduleRoot(t)
	script := readFile(t, filepath.Join(root, "scripts", "test-in-container.sh"))
	ci := readFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	// Only the test-fast job's CONFIGURED step counts: the slice runs from that
	// step's `if:` to the next job, so a key moved to job-level `env:` (which
	// would make the three pristine legs configured too) no longer satisfies it.
	stepStart := strings.Index(ci, "- if: matrix.configured == '1'")
	jobEnd := strings.Index(ci, "\n  test-release:")
	if stepStart < 0 || jobEnd < 0 || jobEnd < stepStart {
		t.Fatalf("ci.yml: could not locate the test-fast configured step (start=%d end=%d)", stepStart, jobEnd)
	}
	configuredStep := ci[stepStart:jobEnd]
	pristineCI := ci[:stepStart] + ci[jobEnd:]
	want := append([]string{"AGENTSYNC_HOME"}, ambientVars...)
	for _, v := range want {
		key := regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(v) + `:\s`)
		if !key.MatchString(configuredStep) {
			t.Errorf(".github/workflows/ci.yml test-fast configured step does not export %s", v)
		}
		if key.MatchString(pristineCI) {
			t.Errorf(".github/workflows/ci.yml exports %s outside the configured step — the pristine legs would no longer be pristine", v)
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
			t.Errorf("scripts/test-in-container.sh: `-e \"%s…\"` must be inside the configured-leg if-block, and only there", v)
		}
	}
	// Outside that block no `-e` may carry a configured-env variable (prose
	// mentions in comments are fine; only the `-e "NAME=` form reaches the container).
	pristine := strings.Replace(script, configuredBlock, "", 1)
	// Both the quoted and unquoted `-e` spellings are checked; `--env` is not
	// used anywhere in this repo's scripts and would be a style break on its own.
	for _, v := range append([]string{"AGENTSYNC_TEST_CONFIGURED_ENV", "AGENTSYNC_HOME"}, ambientVars...) {
		if strings.Contains(pristine, `-e "`+v+`=`) || strings.Contains(pristine, `-e `+v+`=`) {
			t.Errorf("scripts/test-in-container.sh: `-e \"%s=…\"` outside the configured-leg if-block would make the pristine leg configured too", v)
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

// shellIfBlock returns the text from the line that STARTS with header (so a
// comment quoting the header cannot anchor it) through the first following
// line that is exactly "fi" — the first column-0 `fi`, not a bracket-matched
// one, which is enough because every nested `fi` in these scripts is indented;
// a nested `fi` at column 0 would truncate the block and the assertions on its
// tail fail loudly (measured in the #271 review). Guards use it to assert what
// is and is not inside a shell if-block.
func shellIfBlock(t *testing.T, src, header string) string {
	t.Helper()
	loc := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(header)).FindStringIndex(src)
	if loc == nil {
		t.Fatalf("shell source has no line starting with %q", header)
	}
	start := loc[0]
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
	// These three must stay in the entrypoint's stub list; that list is itself
	// kept in step with the probed binaries by TestConfiguredLegFakesEveryAgentBinary
	// (internal/cli), so an agent that stops being probed must be removed here too.
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

// envOverrideDocs are the two hand-maintained environment-override tables that
// claim completeness: README.md's "Environment overrides" and the website's
// reference page. docs/user-guide.md carries a deliberate SUBSET ("the ones
// you'll reach for most") and defers to the README, so it is not checked.
var envOverrideDocs = []string{
	"README.md",
	"website/src/content/docs/reference/environment.mdx",
}

// envOverrideDocExempt names AGENTSYNC_-prefixed string literals in production
// code that are NOT environment variables, so the doc guard does not demand a
// table row for them. Each entry says what the literal is.
var envOverrideDocExempt = map[string]string{
	"AGENTSYNC_LOCAL_HISTORY": "git.NoticeFile — a filename written into versioned destination dirs",
}

// TestEnvOverridesDocumented enforces the completeness claim both tables make
// ("every variable the CLI itself reads"): every environment variable the
// production code reads or names must have a row in BOTH tables, the two tables
// must list the same set, and every row must name a variable the module really
// reads (no phantom rows). PR #272 fixed AGENTSYNC_LOCK_TIMEOUT_MS missing from
// the website page and NO_COLOR / EDITOR missing from both — the second such
// drift; this guard is what turns the doc claim into a checked one.
//
// "Reads or names" is deliberate: an AGENTSYNC_* name inside any production
// string literal counts — an error hint telling the user to set a variable is a
// contract the tables must carry, and the ALLOW_SYMLINK_DEST / NO_UPGRADE_NOTICE
// / AGE_SKIP_PERM_CHECK reads go through package consts a literal-argument scan
// of os.Getenv would miss. Non-AGENTSYNC variables are collected only from a
// direct os.Getenv / os.LookupEnv literal argument (EDITOR, NO_COLOR) or a
// paths.AgentHomeOverride literal argument (GROK_HOME); HOME, read through the
// injected paths.Env, is covered by the AGENTSYNC_TARGET_ROOT row's prose and
// not a table row. The harness's own AGENTSYNC_TEST_* / AGENTSYNC_LIVE_* signals
// are contributor-only (CONTRIBUTING.md lists them) and are exempt, except that
// AGENTSYNC_TEST_IN_CONTAINER may appear in the tables because a user debugging
// a single test is told to set it.
func TestEnvOverridesDocumented(t *testing.T) {
	root := moduleRoot(t)
	read, named, parsed := scanEnvNames(t, root)
	if parsed < 50 {
		t.Fatalf("parsed only %d production files — the walk's filters are skipping the tree", parsed)
	}
	harness := func(name string) bool {
		return strings.HasPrefix(name, "AGENTSYNC_TEST_") || strings.HasPrefix(name, "AGENTSYNC_LIVE_")
	}
	want := map[string]bool{}
	for name := range read {
		if !harness(name) && envOverrideDocExempt[name] == "" {
			want[name] = true
		}
	}
	if len(want) < 10 {
		t.Fatalf("collected only %d production env variables — the scan is not seeing the tree: %v", len(want), sortedKeys(want))
	}
	rowRE := regexp.MustCompile("(?m)^\\| `([A-Z][A-Z0-9_]*)")
	documented := map[string]map[string]bool{} // doc → names
	for _, doc := range envOverrideDocs {
		names := map[string]bool{}
		for _, m := range rowRE.FindAllStringSubmatch(readFile(t, filepath.Join(root, filepath.FromSlash(doc))), -1) {
			names[m[1]] = true
		}
		if len(names) < 10 {
			t.Fatalf("%s: found only %d env-var table rows — the row regexp no longer matches the table", doc, len(names))
		}
		documented[doc] = names
		for _, name := range sortedKeys(want) {
			if !names[name] {
				t.Errorf("%s: production code reads %s but the environment table has no `| `%s` row", doc, name, name)
			}
		}
		for _, name := range sortedKeys(names) {
			if want[name] || name == "AGENTSYNC_TEST_IN_CONTAINER" {
				continue
			}
			if named[name] {
				t.Errorf("%s: row %s names a variable only the test harness reads — contributor-only signals belong in CONTRIBUTING.md", doc, name)
			} else {
				t.Errorf("%s: row %s names a variable nothing in the module reads (phantom row, or a rename the docs missed)", doc, name)
			}
		}
	}
	// The two tables must agree with each other, not merely each cover the code.
	a, b := documented[envOverrideDocs[0]], documented[envOverrideDocs[1]]
	for _, name := range sortedKeys(a) {
		if !b[name] {
			t.Errorf("%s lists %s but %s does not", envOverrideDocs[0], name, envOverrideDocs[1])
		}
	}
	for _, name := range sortedKeys(b) {
		if !a[name] {
			t.Errorf("%s lists %s but %s does not", envOverrideDocs[1], name, envOverrideDocs[0])
		}
	}
}

var agentsyncVarRE = regexp.MustCompile(`\bAGENTSYNC_[A-Z0-9_]+`)

// scanEnvNames walks every non-test Go source in the module and returns the
// environment-variable names production code reads or names (read: outside
// internal/testenv), the names any non-test source in the module names at all
// (named: internal/testenv included, so a harness signal documented in a table
// can be told apart from a phantom), and the number of production files parsed.
func scanEnvNames(t *testing.T, root string) (read, named map[string]bool, parsed int) {
	t.Helper()
	read, named = map[string]bool{}, map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if !strings.Contains(rel, "/") {
				switch d.Name() {
				case ".git", "node_modules", "website", "dist", "bin":
					return filepath.SkipDir
				case "test":
					// e2e / BDD harness support (test/bdd/support reads PATH, TZ
					// and its own AGENTSYNC_BDD_* plumbing): not the CLI.
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		isHarness := strings.HasPrefix(rel, "internal/testenv/")
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return perr
		}
		if !isHarness {
			parsed++
		}
		record := func(name string) {
			named[name] = true
			if !isHarness {
				read[name] = true
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(n.Value)
				if err != nil {
					return true
				}
				for _, name := range agentsyncVarRE.FindAllString(s, -1) {
					record(name)
				}
			case *ast.CallExpr:
				if len(n.Args) == 0 {
					return true
				}
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				// os.Getenv("X") / os.LookupEnv("X") take the name first;
				// paths.AgentHomeOverride(env, "X") takes it last.
				var arg ast.Expr
				switch {
				case pkg.Name == "os" && (sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv") && len(n.Args) == 1:
					arg = n.Args[0]
				case pkg.Name == "paths" && sel.Sel.Name == "AgentHomeOverride" && len(n.Args) == 2:
					arg = n.Args[1]
				default:
					return true
				}
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if name, err := strconv.Unquote(lit.Value); err == nil {
						record(name)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return read, named, parsed
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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

// readFile returns the file's text with CRLF normalized to LF: a Windows
// checkout with core.autocrlf rewrites the shell scripts and YAML, and every
// line-anchored match here (`\nfi\n`, `(?m)^…`) assumes LF. The scripts run
// only on Linux, where they are LF; the guards must not depend on the runner's
// checkout settings.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// TestShellIfBlock_CRLF pins that normalization end to end for the helper the
// Windows test-fast leg tripped on: a CRLF-encoded script must yield the same
// block as its LF twin.
func TestShellIfBlock_CRLF(t *testing.T) {
	lf := "x=1\nif [[ \"$A\" == \"1\" ]]; then\n    if true; then\n        :\n    fi\n    export MARK=1\nfi\necho done\n"
	want := "if [[ \"$A\" == \"1\" ]]; then\n    if true; then\n        :\n    fi\n    export MARK=1\nfi\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	normalized := strings.ReplaceAll(crlf, "\r\n", "\n") // what readFile does
	for name, src := range map[string]string{"lf": lf, "crlf-normalized": normalized} {
		if got := shellIfBlock(t, src, `if [[ "$A" == "1" ]]`); got != want {
			t.Errorf("%s: shellIfBlock = %q, want %q", name, got, want)
		}
	}
}
