package adapter_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// TestSkipKind_StringAndJSON pins the wire contract SkipKind promises: the human
// String() form and, crucially, the explain --json surface (MarshalJSON). It
// covers the invalid zero value too — an unset Kind that somehow escaped the
// guards must serialize to a visible "unset" (surfacing the bug) rather than a
// silent real kind. Without this, nothing proved SkipReduced→"reduced" or the
// zero-value behavior the doc comments promise.
func TestSkipKind_StringAndJSON(t *testing.T) {
	cases := []struct {
		k    adapter.SkipKind
		str  string
		json string
	}{
		{adapter.SkipReduced, "reduced", `"reduced"`},
		{adapter.SkipDropped, "dropped", `"dropped"`},
		{adapter.SkipKindUnset, "unset", `"unset"`},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.str {
			t.Errorf("SkipKind(%d).String() = %q, want %q", int(c.k), got, c.str)
		}
		b, err := json.Marshal(c.k)
		if err != nil {
			t.Fatalf("json.Marshal(%s): %v", c.str, err)
		}
		if string(b) != c.json {
			t.Errorf("json.Marshal(%s) = %s, want %s", c.str, b, c.json)
		}
	}
}

// TestEverySkipLiteralSetsKind is the reachability-INDEPENDENT guard that a skip
// site cannot omit adapter.Skip.Kind. Its sibling TestEveryAdapterClassifiesSkips
// (internal/cli) renders every registered adapter and checks no emitted skip is
// unset — but a render-based check only covers skip sites the fixture actually
// reaches. A site gated on a path that ResolvePaths never leaves empty (e.g.
// windsurf's scope-gap command branch) lives in defensive code the fixture can't
// trigger, so an unset Kind there would slip past unnoticed. This test instead
// parses every production .go file under internal/ and fails if any adapter.Skip
// composite literal omits the Kind field — caught whether or not the site is
// reachable at runtime. The two guards are complementary: this one proves the
// field is always SET; the runtime one proves both kind VALUES are exercised and
// the classification flows through the report.
func TestEverySkipLiteralSetsKind(t *testing.T) {
	root := moduleInternalDir(t)
	fset := token.NewFileSet()
	var (
		offenders []string
		total     int
	)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Production Go only: test files may construct partial Skip literals on
		// purpose (e.g. to exercise the unset/default path).
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		inPkgAdapter := f.Name.Name == "adapter"
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			// Direct `adapter.Skip{...}` (and bare `Skip{...}` only inside package
			// adapter, where Skip is the local type).
			if isSkipType(cl.Type, inPkgAdapter) {
				total++
				if !hasValidKind(cl) {
					offenders = append(offenders, posOf(fset, cl))
				}
				return true
			}
			// `[]adapter.Skip{ {...}, {...} }`: the element literals have an elided
			// type, so reach them through the slice's element type here.
			if at, ok := cl.Type.(*ast.ArrayType); ok && isSkipType(at.Elt, inPkgAdapter) {
				for _, el := range cl.Elts {
					ecl, ok := el.(*ast.CompositeLit)
					if !ok {
						continue
					}
					total++
					if !hasValidKind(ecl) {
						offenders = append(offenders, posOf(fset, ecl))
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scanning internal/ for adapter.Skip literals: %v", err)
	}

	// Anti-vacuity: if the matcher silently stopped matching (e.g. the type was
	// renamed), the "no offenders" pass would be meaningless. The tree holds ~54
	// real skip sites; assert a healthy floor rather than an exact count so adding
	// or removing one site doesn't churn this test.
	if total < 40 {
		t.Fatalf("only matched %d adapter.Skip literals — the matcher likely broke; expected ~54", total)
	}
	for _, o := range offenders {
		t.Errorf("adapter.Skip literal omits Kind (set adapter.SkipReduced or adapter.SkipDropped): %s", o)
	}
}

// TestSkipKindStaticGuardMatchers is the standing self-test for the static guard's
// own logic, so TestEverySkipLiteralSetsKind cannot silently rot into a vacuous
// pass (e.g. a refactor that makes hasValidKind match any field, or isSkipType
// match nothing). It exercises the matchers against synthetic literals — both the
// shapes that MUST be accepted and the ones that MUST be flagged, including the
// explicit-unset case the round-2 review surfaced. This bakes in the
// break-verification CLAUDE.md asks of a guard.
func TestSkipKindStaticGuardMatchers(t *testing.T) {
	lit := func(expr string) *ast.CompositeLit {
		t.Helper()
		e, err := parser.ParseExpr(expr)
		if err != nil {
			t.Fatalf("parse %q: %v", expr, err)
		}
		cl, ok := e.(*ast.CompositeLit)
		if !ok {
			t.Fatalf("%q is not a composite literal", expr)
		}
		return cl
	}

	// hasValidKind: a keyed, non-unset Kind is the only accepted form.
	valid := []string{
		`adapter.Skip{Component: "c", Kind: adapter.SkipReduced}`,
		`adapter.Skip{Component: "c", Kind: adapter.SkipDropped}`,
	}
	flagged := []string{
		`adapter.Skip{Component: "c"}`,                              // Kind omitted
		`adapter.Skip{Component: "c", Kind: adapter.SkipKindUnset}`, // explicit unset
		`adapter.Skip{Component: "c", Kind: 0}`,                     // zero literal
		`adapter.Skip{"c", "n", "r", adapter.SkipDropped}`,          // positional → over-report (safe)
	}
	for _, s := range valid {
		if !hasValidKind(lit(s)) {
			t.Errorf("hasValidKind(%s) = false, want true", s)
		}
	}
	for _, s := range flagged {
		if hasValidKind(lit(s)) {
			t.Errorf("hasValidKind(%s) = true, want false (must be flagged)", s)
		}
	}

	// isSkipType: qualified adapter.Skip always; bare Skip only inside package adapter.
	if !isSkipType(lit(`adapter.Skip{Kind: adapter.SkipDropped}`).Type, false) {
		t.Error("isSkipType(adapter.Skip) = false, want true")
	}
	if isSkipType(lit(`adapter.FileOp{Action: "write"}`).Type, false) {
		t.Error("isSkipType(adapter.FileOp) = true, want false")
	}
	bare := lit(`Skip{Kind: SkipDropped}`).Type
	if !isSkipType(bare, true) {
		t.Error("isSkipType(bare Skip, inPkgAdapter=true) = false, want true")
	}
	if isSkipType(bare, false) {
		t.Error("isSkipType(bare Skip, inPkgAdapter=false) = true, want false")
	}
}

// isSkipType reports whether e is the type of an adapter.Skip composite literal:
// the qualified `adapter.Skip` everywhere, or the bare `Skip` only within package
// adapter itself.
func isSkipType(e ast.Expr, inPkgAdapter bool) bool {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		x, ok := t.X.(*ast.Ident)
		return ok && x.Name == "adapter" && t.Sel.Name == "Skip"
	case *ast.Ident:
		return inPkgAdapter && t.Name == "Skip"
	}
	return false
}

// hasValidKind reports whether a composite literal sets the Kind field by name to
// a value that is NOT the invalid zero (SkipKindUnset / 0). Checking only that the
// field name appears would let an explicit `Kind: adapter.SkipKindUnset` (or
// `Kind: 0`) pass while being exactly the invalid state the guard forbids — and a
// site like that in unreachable defensive code would slip past the runtime guard
// too. A positional literal (no keyed Kind) returns false and is flagged; the
// codebase convention is keyed fields, so this only ever over-reports, never
// under-reports.
func hasValidKind(cl *ast.CompositeLit) bool {
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Kind" {
			return !isUnsetKindValue(kv.Value)
		}
	}
	return false
}

// isUnsetKindValue reports whether an expression denotes the invalid zero value:
// `adapter.SkipKindUnset`, bare `SkipKindUnset` (inside package adapter), or the
// integer literal `0`.
func isUnsetKindValue(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		x, ok := v.X.(*ast.Ident)
		return ok && x.Name == "adapter" && v.Sel.Name == "SkipKindUnset"
	case *ast.Ident:
		return v.Name == "SkipKindUnset"
	case *ast.BasicLit:
		return v.Kind == token.INT && v.Value == "0"
	}
	return false
}

func posOf(fset *token.FileSet, n ast.Node) string {
	p := fset.Position(n.Pos())
	return fmt.Sprintf("%s:%d", filepath.Base(filepath.Dir(p.Filename))+"/"+filepath.Base(p.Filename), p.Line)
}

// moduleInternalDir locates the repo's internal/ directory from this test file's
// own path, so the scan is independent of the test's working directory.
func moduleInternalDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate the source tree")
	}
	// thisFile = <root>/internal/adapter/skipkind_test.go → <root>/internal
	internalDir := filepath.Dir(filepath.Dir(thisFile))
	if filepath.Base(internalDir) != "internal" {
		t.Fatalf("expected to resolve internal/, got %q", internalDir)
	}
	return internalDir
}
