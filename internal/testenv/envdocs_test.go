package testenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// envOverrideDoc is one hand-maintained environment-override table that claims
// completeness. heading is the Markdown heading that opens the section holding
// the table; rows are read from there to the next heading OF ANY LEVEL, HTML
// comment or JSX tag (envSectionEndRE), so other tables in the same file — the website page's
// "Common" subset, a future `### Deprecated` table tucked under the heading, any
// table with backticked ALL-CAPS first cells — are neither counted nor able to
// satisfy the guard. The table has to sit directly under its heading.
type envOverrideDoc struct {
	path, heading string
}

// envSectionEndRE ends a doc section: any Markdown heading (`#` to `######`,
// so a sub-heading inside the section closes it too — measured in the #272
// review: a `### ` table under the heading otherwise satisfied the guard) or a
// line opening with `<` — a JSX tag (the mdx page's `<Aside>` follows its
// table) or an HTML comment.
var envSectionEndRE = regexp.MustCompile(`(?m)^(#{1,6} |<)`)

// envOverrideDocs are the two complete tables.
var envOverrideDocs = []envOverrideDoc{
	{path: "README.md", heading: "## Environment overrides"},
	{path: "website/src/content/docs/reference/environment.mdx", heading: "## All overrides"},
}

// envOverrideSubsetDocs are the tables that declare themselves a SUBSET of the
// complete ones ("the ones you'll reach for most") and defer to them for the
// rest. TestEnvOverrideSubsetsAreSubsets holds them to that: every row must be
// in both complete tables, and the table must be strictly smaller, or it has
// silently become a third full copy that nothing keeps complete.
var envOverrideSubsetDocs = []envOverrideDoc{
	{path: "docs/user-guide.md", heading: "## Troubleshooting & environment overrides"},
	{path: "website/src/content/docs/reference/environment.mdx", heading: "## Common"},
}

// envOverrideDocExempt names AGENTSYNC_-prefixed string literals in production
// code that are NOT environment variables, so the guard does not demand a table
// row for them. Each entry says what the literal is. An entry whose name the
// scan no longer finds fails the test: an allowlist that has gone stale reads as
// coverage while pinning nothing (cf. internal/cli's renamed-commands guard).
var envOverrideDocExempt = map[string]string{
	"AGENTSYNC_LOCAL_HISTORY": "git.NoticeFile — a filename written into versioned destination dirs",
}

// envOverrideDocRequired is the mirror image of envOverrideDocExempt: rows the
// tables must carry although no production code reads the variable, because
// the docs promise them. Each entry says why. Unlike the exemptions these
// cannot be staleness-checked against the scan (the scan skips internal/testenv,
// the only reader), so an entry here is kept honest by CONTRIBUTING.md's
// harness table naming the same variable.
var envOverrideDocRequired = map[string]string{
	"AGENTSYNC_TEST_IN_CONTAINER": "the one harness signal a user debugging a single test is told to set; README and CONTRIBUTING promise the row",
}

// TestEnvOverridesDocumented enforces the completeness claim both tables make
// (every environment override the CLI honours, AGENTSYNC_* or not): every such
// variable production code reads or names must have a row in BOTH tables, the
// two tables must list the same set, and every row must name a variable
// production code really reads (no phantom rows, no harness-only signals). PR
// #272 fixed AGENTSYNC_LOCK_TIMEOUT_MS missing from the website page and
// NO_COLOR / EDITOR missing from both — the second such drift; this guard is
// what turns the doc claim into a checked one.
//
// "Reads or names" is deliberate: an AGENTSYNC_* name inside any production
// string literal counts — an error hint telling the user to set a variable is a
// contract the tables must carry, and the ALLOW_SYMLINK_DEST / NO_UPGRADE_NOTICE
// / AGE_SKIP_PERM_CHECK reads go through package consts that a literal-argument
// scan of os.Getenv would miss. Non-AGENTSYNC variables are collected only from
// a direct os.Getenv / os.LookupEnv literal argument (EDITOR, NO_COLOR) or a
// paths.AgentHomeOverride literal argument (GROK_HOME).
//
// Accepted residuals, written down so nobody mistakes them for coverage: a
// non-AGENTSYNC variable read through a const or a stored func value is
// invisible (every such read today is a literal); standard process-environment
// conventions honoured by Go and its libraries rather than by agentsync — HOME
// (through the injected paths.Env), PATH (exec.LookPath), TMPDIR / TMP / TEMP
// (os.TempDir: `secret edit`'s decrypted vault, plugin scratch dirs),
// HTTP_PROXY / HTTPS_PROXY / NO_PROXY (net/http and go-git fetches),
// SSH_AUTH_SOCK (go-git ssh), cobra's completion-time AGENTSYNC_ACTIVE_HELP
// (a name it builds at runtime) — are described in the prose as such, not
// rows; `${env:NAME}` references resolve whatever the user names and are a
// feature, not an override; and only the NAME SET is checked — a row's
// description can drift freely (the declared-subset tables are held to the
// name set too, by TestEnvOverrideSubsetsAreSubsets). The harness's own AGENTSYNC_TEST_* /
// AGENTSYNC_LIVE_* signals are contributor-only (CONTRIBUTING.md lists them)
// and are rejected as rows, except AGENTSYNC_TEST_IN_CONTAINER, which a user
// debugging a single test is told to set and which the prose promises, so it
// is REQUIRED in both tables (envOverrideDocRequired).
func TestEnvOverridesDocumented(t *testing.T) {
	root := moduleRoot(t)
	found, parsed := scanEnvNamesForDocs(t, root)
	if parsed < 50 {
		t.Fatalf("parsed only %d production files — the walk's filters are skipping the tree", parsed)
	}
	for name, what := range envOverrideDocExempt {
		if !found[name] {
			t.Errorf("envOverrideDocExempt[%q] (%s) matches nothing in production code any more — remove the stale entry", name, what)
		}
	}
	harness := func(name string) bool {
		return strings.HasPrefix(name, "AGENTSYNC_TEST_") || strings.HasPrefix(name, "AGENTSYNC_LIVE_")
	}
	want := map[string]bool{}
	for name := range found {
		if !harness(name) && envOverrideDocExempt[name] == "" {
			want[name] = true
		}
	}
	if len(want) < minEnvTableRows {
		t.Fatalf("collected only %d production env variables — the scan is not seeing the tree: %v", len(want), sortedKeys(want))
	}
	for name := range envOverrideDocRequired { // after the floor, so a promised row never pads the scan's count
		want[name] = true
	}
	documented := make([]map[string]bool, len(envOverrideDocs))
	for i, doc := range envOverrideDocs {
		names := envTableRows(t, doc, readFile(t, filepath.Join(root, filepath.FromSlash(doc.path))), minEnvTableRows)
		documented[i] = names
		for _, name := range sortedKeys(want) {
			if !names[name] {
				why := "production code reads or names it"
				if r := envOverrideDocRequired[name]; r != "" {
					why = r
				}
				t.Errorf("%s: %s needs a row in the %q table (%s) — none found", doc.path, name, doc.heading, why)
			}
		}
		for _, name := range sortedKeys(names) {
			switch {
			case want[name]:
			case harness(name):
				t.Errorf("%s: row %s is a test-harness signal — contributor-only signals belong in CONTRIBUTING.md, not the user tables", doc.path, name)
			case envOverrideDocExempt[name] != "":
				t.Errorf("%s: row %s is not an environment variable (%s) — drop the row", doc.path, name, envOverrideDocExempt[name])
			default:
				t.Errorf("%s: row %s names a variable production code neither reads nor names (phantom row, or a rename the docs missed)", doc.path, name)
			}
		}
	}
	// The two tables must agree with each other, not merely each cover the code.
	a, b := documented[0], documented[1]
	for _, name := range sortedKeys(a) {
		if !b[name] {
			t.Errorf("%s lists %s but %s does not", envOverrideDocs[0].path, name, envOverrideDocs[1].path)
		}
	}
	for _, name := range sortedKeys(b) {
		if !a[name] {
			t.Errorf("%s lists %s but %s does not", envOverrideDocs[1].path, name, envOverrideDocs[0].path)
		}
	}
}

// TestEnvOverrideSubsetsAreSubsets enforces what the subset tables say about
// themselves: a row there names a variable BOTH complete tables carry (so a
// rename or removal reaches the subsets too — the user guide silently kept
// rows that the #272 review had to reconcile by hand), and the subset stays
// strictly smaller than the complete tables, so "the ones you'll reach for
// most" cannot drift into an unguarded full copy.
func TestEnvOverrideSubsetsAreSubsets(t *testing.T) {
	root := moduleRoot(t)
	complete := make([]map[string]bool, len(envOverrideDocs))
	for i, doc := range envOverrideDocs {
		complete[i] = envTableRows(t, doc, readFile(t, filepath.Join(root, filepath.FromSlash(doc.path))), minEnvTableRows)
	}
	for _, doc := range envOverrideSubsetDocs {
		rows := envTableRows(t, doc, readFile(t, filepath.Join(root, filepath.FromSlash(doc.path))), 1)
		for _, name := range sortedKeys(rows) {
			for i, full := range complete {
				if !full[name] {
					t.Errorf("%s (%q): row %s is not in the complete table %s — the subset lists a variable the full reference does not", doc.path, doc.heading, name, envOverrideDocs[i].path)
				}
			}
		}
		for _, full := range complete {
			if len(rows) >= len(full) {
				t.Errorf("%s (%q): %d rows is not a strict subset of the %d-row complete table — it has become a full copy nothing keeps complete", doc.path, doc.heading, len(rows), len(full))
			}
		}
	}
}

// envTableRowRE matches a table row whose first cell opens with a backticked
// ALL-CAPS identifier and captures the identifier (`| `AGENTSYNC_X=1` | …` → AGENTSYNC_X).
var envTableRowRE = regexp.MustCompile("(?m)^\\| `([A-Z][A-Z0-9_]*)")

// minEnvTableRows is the vacuity floor for both the scan and each table: well
// under today's count (15 rows, 15 variables) but far above what a broken
// regexp or a truncated section yields, so a silent narrowing fails loudly.
const minEnvTableRows = 10

// envTableRows returns the variable names in doc's table: the rows between its
// heading and the next Markdown heading, HTML comment or JSX tag (`<Aside`). Fails loudly if
// the heading is missing or the section holds fewer than minRows rows
// (minEnvTableRows for a complete table; 1 for a declared subset).
func envTableRows(t *testing.T, doc envOverrideDoc, text string, minRows int) map[string]bool {
	t.Helper()
	start := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(doc.heading) + `\s*$`).FindStringIndex(text)
	if start == nil {
		t.Fatalf("%s: no %q heading — the env table moved or was renamed", doc.path, doc.heading)
	}
	section := text[start[1]:]
	if end := envSectionEndRE.FindStringIndex(section); end != nil {
		section = section[:end[0]]
	}
	names := map[string]bool{}
	for _, m := range envTableRowRE.FindAllStringSubmatch(section, -1) {
		names[m[1]] = true
	}
	if len(names) < minRows {
		t.Fatalf("%s: found only %d env-var rows under %q — the table must sit directly under its heading (a heading of any level, an HTML comment or a JSX tag at line start ends the section), or the row format changed so `| `NAME` no longer opens each row", doc.path, len(names), doc.heading)
	}
	return names
}

var agentsyncVarRE = regexp.MustCompile(`\bAGENTSYNC_[A-Z0-9_]+`)

// scanEnvNamesForDocs walks every non-test Go source the CLI is built from and
// returns the environment-variable names it reads or names, plus the number of
// files parsed. It is a sibling of scanEnvReads (guards_test.go) with a
// different question: that walker asks WHERE raw env reads happen (per package,
// for the scrub rule) and which agent-home variables they touch; this one asks
// WHICH names appear anywhere (any AGENTSYNC_* match in a string literal, plus
// literal os.Getenv / os.LookupEnv / paths.AgentHomeOverride arguments). Both
// skip the same top-level non-CLI trees; this one also skips internal/testenv
// (the harness, whose literals are its own signals) and test/ (e2e / BDD
// support, which reads PATH, TZ and its own AGENTSYNC_BDD_* plumbing).
func scanEnvNamesForDocs(t *testing.T, root string) (found map[string]bool, parsed int) {
	t.Helper()
	found = map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if !strings.Contains(rel, "/") {
				switch d.Name() {
				case ".git", "node_modules", "website", "dist", "bin", "test":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasPrefix(rel, "internal/testenv/") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return perr
		}
		parsed++
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
					found[name] = true
				}
			case *ast.CallExpr:
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
						found[name] = true
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
	return found, parsed
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
