package testenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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
// It also enforces the scrub's reach: any production package that reads the
// environment at all (any os.Getenv / os.LookupEnv call) must have a test file
// importing internal/testenv, or its tests run without the ambient scrub and
// can depend on the developer's shell.
//
// Known residual, deliberately accepted: the agent-home check matches only a
// string-literal argument, so `os.Getenv(someConst)` evades it. Every raw env
// read in this repo uses a literal or a package const named *Env for the
// harness's OWN knobs; a new agent-home read would be reviewed against
// testenv.agentHomeVars regardless.
func TestAgentHomeVarsReadOnlyThroughPaths(t *testing.T) {
	root := moduleRoot(t)
	var offenders []string
	envReaders := map[string]bool{} // package dir (module-relative) → reads env raw
	parsed := 0
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
	// Vacuity guard: the walk must actually have parsed the production tree.
	if parsed < 50 {
		t.Fatalf("parsed only %d production files — the walk's filters are skipping the tree", parsed)
	}
	if len(offenders) != 0 {
		t.Fatalf("agent home variables must be read through paths.AgentHomeOverride, never raw:\n  %s",
			strings.Join(offenders, "\n  "))
	}
	for dir := range envReaders {
		hasTests, imports := dirTestsImportTestenv(t, filepath.Join(root, filepath.FromSlash(dir)))
		// A package with no _test.go files has no test binary of its own to
		// scrub (test/bdd/support is compiled only into the bdd binary, whose
		// TestMain imports testenv), so the requirement is per test binary.
		if hasTests && !imports {
			t.Errorf("%s reads the environment raw but none of its _test.go files import internal/testenv, so its tests run unscrubbed (add `import _ \"github.com/spxrogers/agentsync/internal/testenv\"` or a guard call)", dir)
		}
	}
}

// dirTestsImportTestenv reports whether dir has any _test.go files and whether
// one of them imports this package (directly; the guard is per test binary, not
// transitive).
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
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "github.com/spxrogers/agentsync/internal/testenv" {
				return true, true
			}
		}
	}
	return hasTests, false
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
	want := append([]string{"AGENTSYNC_HOME"}, ambientVars...)
	for _, v := range want {
		if !strings.Contains(script, `-e "`+v+`=`) {
			t.Errorf("scripts/test-in-container.sh configured leg does not export %s", v)
		}
		if !regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(v) + `:\s`).MatchString(ci) {
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
