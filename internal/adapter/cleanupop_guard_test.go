package adapter_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestEveryCleanupLiteralUsesNewCleanupOp is the reachability-independent guard
// behind NewCleanupOp's "only producer of OpCleanup" promise. apply identifies
// a cleanup op by Kind, never by shape, so a synthesis site that hand-rolls the
// shape — a FileOp literal whose Content is the empty object "{}" — and forgets
// the stamp would relabel every key removal as a write, and on the purge path
// (where nothing reads Kind) no test would notice. This test parses every
// production .go file under internal/ and fails on any FileOp composite literal
// whose Content is a []byte("{}") literal outside NewCleanupOp itself. Sibling
// of TestEverySkipLiteralSetsKind: same scan, same matcher self-test.
func TestEveryCleanupLiteralUsesNewCleanupOp(t *testing.T) {
	root := moduleInternalDir(t)
	fset := token.NewFileSet()
	var (
		offenders []string
		total     int // FileOp literals matched, for the anti-vacuity floor
	)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Production Go only: test fixtures build "{}" key-merge ops on purpose.
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		inPkgAdapter := f.Name.Name == "adapter"
		// The one permitted site is the body of NewCleanupOp in package adapter.
		var allowStart, allowEnd token.Pos
		if inPkgAdapter {
			for _, decl := range f.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "NewCleanupOp" {
					allowStart, allowEnd = fn.Pos(), fn.End()
				}
			}
		}
		check := func(cl *ast.CompositeLit) {
			total++
			inAllowed := allowStart.IsValid() && cl.Pos() >= allowStart && cl.Pos() < allowEnd
			if hasEmptyObjectContent(cl) && !inAllowed {
				offenders = append(offenders, posOf(fset, cl))
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if isFileOpType(cl.Type, inPkgAdapter) {
				check(cl)
				return true
			}
			// `[]adapter.FileOp{ {...}, {...} }`: elided element types.
			if at, ok := cl.Type.(*ast.ArrayType); ok && isFileOpType(at.Elt, inPkgAdapter) {
				for _, el := range cl.Elts {
					if ecl, ok := el.(*ast.CompositeLit); ok {
						check(ecl)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scanning internal/ for adapter.FileOp literals: %v", err)
	}

	// Anti-vacuity: the tree holds ~47 production FileOp literals (41 adapter
	// Render sites plus the pipeline's and cli's synthesized ops). A healthy
	// floor rather than an exact count, so adding or removing a site doesn't
	// churn this test.
	if total < 30 {
		t.Fatalf("only matched %d adapter.FileOp literals — the matcher likely broke; expected ~47", total)
	}
	for _, o := range offenders {
		t.Errorf("FileOp literal hand-rolls the \"{}\" cleanup shape without the OpCleanup stamp (call adapter.NewCleanupOp): %s", o)
	}
}

// TestCleanupOpStaticGuardMatchers is the standing self-test for the guard's
// matchers, so a refactor that silently stops them matching cannot turn the
// guard into a vacuous pass.
func TestCleanupOpStaticGuardMatchers(t *testing.T) {
	lit := func(src string) *ast.CompositeLit {
		t.Helper()
		e, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatalf("ParseExpr(%q): %v", src, err)
		}
		cl, ok := e.(*ast.CompositeLit)
		if !ok {
			t.Fatalf("ParseExpr(%q): not a composite literal", src)
		}
		return cl
	}
	tests := []struct {
		name         string
		src          string
		inPkgAdapter bool
		wantFileOp   bool
		wantEmptyObj bool
	}{
		{name: "qualified, double-quoted {}", src: `adapter.FileOp{Content: []byte("{}")}`, wantFileOp: true, wantEmptyObj: true},
		{name: "qualified, raw-string {}", src: "adapter.FileOp{Content: []byte(`{}`)}", wantFileOp: true, wantEmptyObj: true},
		{name: "whitespace around {} is still the shape", src: `adapter.FileOp{Content: []byte(" {}\n")}`, wantFileOp: true, wantEmptyObj: true},
		{name: "populated content is not the shape", src: `adapter.FileOp{Content: []byte("{\"a\":1}")}`, wantFileOp: true, wantEmptyObj: false},
		{name: "non-literal content is not statically the shape", src: `adapter.FileOp{Content: body}`, wantFileOp: true, wantEmptyObj: false},
		{name: "no Content at all", src: `adapter.FileOp{Path: "x"}`, wantFileOp: true, wantEmptyObj: false},
		{name: "bare FileOp inside package adapter", src: `FileOp{Content: []byte("{}")}`, inPkgAdapter: true, wantFileOp: true, wantEmptyObj: true},
		{name: "bare FileOp outside package adapter is some other type", src: `FileOp{Content: []byte("{}")}`, wantFileOp: false},
		{name: "a Skip literal is not a FileOp", src: `adapter.Skip{Kind: adapter.SkipDropped}`, wantFileOp: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := lit(tt.src)
			if got := isFileOpType(cl.Type, tt.inPkgAdapter); got != tt.wantFileOp {
				t.Fatalf("isFileOpType = %v, want %v", got, tt.wantFileOp)
			}
			if !tt.wantFileOp {
				return
			}
			if got := hasEmptyObjectContent(cl); got != tt.wantEmptyObj {
				t.Errorf("hasEmptyObjectContent = %v, want %v", got, tt.wantEmptyObj)
			}
		})
	}
}

// isFileOpType reports whether e is the type of an adapter.FileOp composite
// literal: the qualified `adapter.FileOp` everywhere, or the bare `FileOp` only
// within package adapter itself.
func isFileOpType(e ast.Expr, inPkgAdapter bool) bool {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		x, ok := t.X.(*ast.Ident)
		return ok && x.Name == "adapter" && t.Sel.Name == "FileOp"
	case *ast.Ident:
		return inPkgAdapter && t.Name == "FileOp"
	}
	return false
}

// hasEmptyObjectContent reports whether a FileOp literal sets Content to the
// static empty object — `[]byte("{}")` or `[]byte(`{}`)`, whitespace-trimmed
// like the writer sees it. Content built from a variable or a call is not a
// static shape and is not matched; the guard is deliberately literal-only,
// like its Skip sibling.
func hasEmptyObjectContent(cl *ast.CompositeLit) bool {
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); !ok || id.Name != "Content" {
			continue
		}
		call, ok := kv.Value.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return false
		}
		at, ok := call.Fun.(*ast.ArrayType)
		if !ok || at.Len != nil {
			return false
		}
		if elt, ok := at.Elt.(*ast.Ident); !ok || elt.Name != "byte" {
			return false
		}
		bl, ok := call.Args[0].(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return false
		}
		s, err := strconv.Unquote(bl.Value)
		return err == nil && strings.TrimSpace(s) == "{}"
	}
	return false
}
