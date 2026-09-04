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
// behind NewCleanupOp's "only producer of OpCleanup" promise, in both
// directions. apply identifies a cleanup op by Kind, never by shape, so a
// synthesis site that hand-rolls the shape — a FileOp literal whose Content is
// the static empty object "{}" going into a key-merge destination (it names a
// MergeStrategy or OwnedKeys) — and forgets the stamp would relabel every key
// removal as a write, and on the purge path (where nothing reads Kind) no test
// would notice; a literal that stamps Kind: OpCleanup by hand is the other way
// around the constructor. This test parses every production .go file under
// internal/ and fails on either, anywhere outside NewCleanupOp's own body. A
// whole-file write of "{}" (no strategy, no owned keys) is not the cleanup
// shape and passes. Deliberately literal-only, like TestEverySkipLiteralSetsKind:
// Content built from a variable or a call, or assigned after construction, is
// not a static shape, and the runtime tier for the pipeline path is
// TestApplyDryRun_CleanupOpNotCountedToWrite.
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
		n, off := scanCleanupLiterals(fset, f)
		total += n
		offenders = append(offenders, off...)
		return nil
	})
	if err != nil {
		t.Fatalf("scanning internal/ for adapter.FileOp literals: %v", err)
	}

	// Anti-vacuity: the tree holds ~45 production FileOp literals (41 adapter
	// Render sites plus the synthesized delete/orphan ops and NewCleanupOp's
	// own). A healthy floor rather than an exact count, so adding or removing
	// a site doesn't churn this test; the count is logged so a drift is visible
	// under -v.
	t.Logf("matched %d production FileOp literals", total)
	if total < 30 {
		t.Fatalf("only matched %d adapter.FileOp literals — the matcher likely broke; expected ~45", total)
	}
	for _, o := range offenders {
		t.Error(o)
	}
}

// scanCleanupLiterals walks one parsed production file and reports every FileOp
// composite literal — direct, or a type-elided element of a slice, array or map
// of FileOp (nested containers included) — that bypasses NewCleanupOp: one that hand-rolls the cleanup shape, one that
// stamps Kind: OpCleanup itself, or a positional literal the matchers cannot
// read (flagged in the safe direction, like the Skip guard). Literals inside
// NewCleanupOp's own body, in package adapter, are the one exempt site. It also
// returns how many FileOp literals it matched, for the caller's anti-vacuity
// floor.
func scanCleanupLiterals(fset *token.FileSet, f *ast.File) (total int, offenders []string) {
	inPkgAdapter := f.Name.Name == "adapter"
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
		if allowStart.IsValid() && cl.Pos() >= allowStart && cl.Pos() < allowEnd {
			return
		}
		switch {
		case isPositionalLiteral(cl):
			offenders = append(offenders, posOf(fset, cl)+": positional FileOp literal — use keyed fields so the cleanup-shape guard can read it")
		case stampsOpCleanup(cl):
			offenders = append(offenders, posOf(fset, cl)+": FileOp literal stamps Kind: OpCleanup by hand — call adapter.NewCleanupOp, its only producer")
		case hasCleanupShape(cl):
			offenders = append(offenders, posOf(fset, cl)+": FileOp literal hand-rolls the cleanup shape (an empty \"{}\" object into a key-merge destination) without the OpCleanup stamp — call adapter.NewCleanupOp; a whole-file write of \"{}\" names neither MergeStrategy nor OwnedKeys and is not flagged")
		}
	}
	// checkElided walks a container literal whose element literals elide
	// their type — `[]adapter.FileOp{{…}}`, `map[string]adapter.FileOp{"k": {…}}`,
	// or a nesting of those — and checks each FileOp element it reaches. An
	// element that spells its own type is left to the plain Inspect branch
	// below, so nothing is counted twice.
	var checkElided func(cl *ast.CompositeLit, elt ast.Expr)
	checkElided = func(cl *ast.CompositeLit, elt ast.Expr) {
		for _, el := range cl.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				el = kv.Value
			}
			ecl, ok := el.(*ast.CompositeLit)
			if !ok || ecl.Type != nil {
				continue
			}
			if isFileOpType(elt, inPkgAdapter) {
				check(ecl)
			} else if inner := containerElem(elt); inner != nil {
				checkElided(ecl, inner)
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if isFileOpType(cl.Type, inPkgAdapter) {
			check(cl)
		} else if elt := containerElem(cl.Type); elt != nil {
			checkElided(cl, elt)
		}
		return true
	})
	return total, offenders
}

// containerElem returns the element type of a slice or array, or the value
// type of a map, or nil for any other type expression.
func containerElem(t ast.Expr) ast.Expr {
	switch c := t.(type) {
	case *ast.ArrayType:
		return c.Elt
	case *ast.MapType:
		return c.Value
	}
	return nil
}

// TestCleanupOpStaticGuardScan pins the scan on parsed snippets — above all the
// NewCleanupOp allow-window, which the matcher rows below cannot see: a literal
// inside NewCleanupOp's body is exempt, the same literal in any other function
// (or another package's NewCleanupOp) is not, and widening the window to the
// whole file would let the second case through unreported.
func TestCleanupOpStaticGuardScan(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		wantTotal int
		wantMsgs  []string // one substring per expected offender, in order
	}{
		{
			name: "inside NewCleanupOp is the one allowed site",
			src: `package adapter
func NewCleanupOp(p, s string, o []string) FileOp { return FileOp{Kind: OpCleanup, Content: []byte("{}"), MergeStrategy: s, OwnedKeys: o} }`,
			wantTotal: 1,
		},
		{
			name: "the same shape in another adapter function is flagged",
			src: `package adapter
func NewCleanupOp(p, s string, o []string) FileOp { return FileOp{Kind: OpCleanup, Content: []byte("{}"), MergeStrategy: s, OwnedKeys: o} }
func other(s string) FileOp { return FileOp{Content: []byte("{}"), MergeStrategy: s} }`,
			wantTotal: 2,
			wantMsgs:  []string{"hand-rolls the cleanup shape"},
		},
		{
			name: "a NewCleanupOp in another package grants no window",
			src: `package other
func NewCleanupOp(s string) adapter.FileOp { return adapter.FileOp{Content: []byte("{}"), MergeStrategy: s} }`,
			wantTotal: 1,
			wantMsgs:  []string{"hand-rolls the cleanup shape"},
		},
		{
			name: "hand-stamped Kind outside the constructor is flagged",
			src: `package render
func f(body []byte) adapter.FileOp { return adapter.FileOp{Kind: adapter.OpCleanup, Content: body} }`,
			wantTotal: 1,
			wantMsgs:  []string{"stamps Kind: OpCleanup by hand"},
		},
		{
			name: "elided slice element is reached and flagged",
			src: `package cli
var ops = []adapter.FileOp{{Action: adapter.ActionWrite, Content: []byte(` + "`{}`" + `), OwnedKeys: ptrs}}`,
			wantTotal: 1,
			wantMsgs:  []string{"hand-rolls the cleanup shape"},
		},
		{
			name: "an explicitly typed slice element is counted once",
			src: `package cli
var ops = []adapter.FileOp{adapter.FileOp{Content: []byte("{}"), OwnedKeys: ptrs}}`,
			wantTotal: 1,
			wantMsgs:  []string{"hand-rolls the cleanup shape"},
		},
		{
			name: "elided map value is reached",
			src: `package cli
var byPath = map[string]adapter.FileOp{"a": {Content: []byte("{}"), OwnedKeys: ptrs}}`,
			wantTotal: 1,
			wantMsgs:  []string{"hand-rolls the cleanup shape"},
		},
		{
			name: "elided element of a nested slice is reached",
			src: `package cli
var batches = [][]adapter.FileOp{{{Content: []byte("{}"), MergeStrategy: s}}}`,
			wantTotal: 1,
			wantMsgs:  []string{"hand-rolls the cleanup shape"},
		},
		{
			name: "a method named NewCleanupOp grants no window",
			src: `package adapter
type T struct{}
func (T) NewCleanupOp(s string) FileOp { return FileOp{Content: []byte("{}"), MergeStrategy: s} }`,
			wantTotal: 1,
			wantMsgs:  []string{"hand-rolls the cleanup shape"},
		},
		{
			name: "a whole-file {} write is not the cleanup shape",
			src: `package x
var op = adapter.FileOp{Action: adapter.ActionWrite, Path: p, Content: []byte("{}"), Mode: 0o644}`,
			wantTotal: 1,
		},
		{
			name: "positional literal is flagged in the safe direction",
			src: `package adapter
var op = FileOp{ActionWrite, OpRender, "p", nil, 0, "", "", nil}`,
			wantTotal: 1,
			wantMsgs:  []string{"positional FileOp literal"},
		},
		{
			name: "a Skip literal is not counted",
			src: `package x
var s = adapter.Skip{Kind: adapter.SkipDropped}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "snippet.go", tt.src, 0)
			if err != nil {
				t.Fatalf("parse snippet: %v", err)
			}
			total, offenders := scanCleanupLiterals(fset, f)
			if total != tt.wantTotal {
				t.Errorf("total = %d, want %d", total, tt.wantTotal)
			}
			if len(offenders) != len(tt.wantMsgs) {
				t.Fatalf("offenders = %q, want %d: %q", offenders, len(tt.wantMsgs), tt.wantMsgs)
			}
			for i, want := range tt.wantMsgs {
				if !strings.Contains(offenders[i], want) {
					t.Errorf("offender %d = %q, want it to mention %q", i, offenders[i], want)
				}
			}
		})
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
		name           string
		src            string
		inPkgAdapter   bool
		wantFileOp     bool
		wantEmptyObj   bool
		wantShape      bool
		wantStamp      bool
		wantPositional bool
	}{
		{name: "{} into a key-merge strategy is the shape", src: `adapter.FileOp{Content: []byte("{}"), MergeStrategy: strat}`, wantFileOp: true, wantEmptyObj: true, wantShape: true},
		{name: "{} with owned keys is the shape", src: `adapter.FileOp{Content: []byte("{}"), OwnedKeys: ptrs}`, wantFileOp: true, wantEmptyObj: true, wantShape: true},
		{name: "raw-string {} counts", src: "adapter.FileOp{Content: []byte(`{}`), OwnedKeys: ptrs}", wantFileOp: true, wantEmptyObj: true, wantShape: true},
		{name: "interior and edge whitespace do not hide {}", src: `adapter.FileOp{Content: []byte(" {\n }\n"), OwnedKeys: ptrs}`, wantFileOp: true, wantEmptyObj: true, wantShape: true},
		{name: "{} as a whole-file write is not the shape", src: `adapter.FileOp{Path: p, Content: []byte("{}"), Mode: 0o644}`, wantFileOp: true, wantEmptyObj: true},
		{name: "{} with an explicit replace strategy is not the shape", src: `adapter.FileOp{Content: []byte("{}"), MergeStrategy: "replace"}`, wantFileOp: true, wantEmptyObj: true},
		{name: "populated content is not the shape", src: `adapter.FileOp{Content: []byte("{\"a\":1}"), MergeStrategy: strat}`, wantFileOp: true},
		{name: "non-literal content is not statically the shape", src: `adapter.FileOp{Content: body, MergeStrategy: strat}`, wantFileOp: true},
		{name: "hand-stamped OpCleanup", src: `adapter.FileOp{Kind: adapter.OpCleanup, Content: body}`, wantFileOp: true, wantStamp: true},
		{name: "bare OpCleanup inside package adapter", src: `FileOp{Kind: OpCleanup}`, inPkgAdapter: true, wantFileOp: true, wantStamp: true},
		{name: "OpRender is not a hand stamp", src: `adapter.FileOp{Kind: adapter.OpRender}`, wantFileOp: true},
		{name: "positional literal", src: `adapter.FileOp{adapter.ActionWrite, adapter.OpRender, "p", nil, 0, "", "", nil}`, wantFileOp: true, wantPositional: true},
		{name: "empty literal is keyed enough", src: `adapter.FileOp{}`, wantFileOp: true},
		{name: "bare FileOp inside package adapter", src: `FileOp{Content: []byte("{}"), OwnedKeys: o}`, inPkgAdapter: true, wantFileOp: true, wantEmptyObj: true, wantShape: true},
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
			if got := hasCleanupShape(cl); got != tt.wantShape {
				t.Errorf("hasCleanupShape = %v, want %v", got, tt.wantShape)
			}
			if got := stampsOpCleanup(cl); got != tt.wantStamp {
				t.Errorf("stampsOpCleanup = %v, want %v", got, tt.wantStamp)
			}
			if got := isPositionalLiteral(cl); got != tt.wantPositional {
				t.Errorf("isPositionalLiteral = %v, want %v", got, tt.wantPositional)
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

// keyedField returns the value of the named keyed field in a composite literal,
// or nil when the literal does not set it.
func keyedField(cl *ast.CompositeLit, name string) ast.Expr {
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name {
			return kv.Value
		}
	}
	return nil
}

// isPositionalLiteral reports whether a non-empty composite literal has any
// unkeyed element. The matchers read keyed fields only, so such a literal is
// flagged rather than silently passed; go vet already rejects the positional
// form for the imported adapter.FileOp, so this only bites inside package
// adapter, where the codebase convention is keyed fields anyway.
func isPositionalLiteral(cl *ast.CompositeLit) bool {
	for _, el := range cl.Elts {
		if _, ok := el.(*ast.KeyValueExpr); !ok {
			return true
		}
	}
	return false
}

// stampsOpCleanup reports whether a FileOp literal sets Kind to OpCleanup
// itself — `adapter.OpCleanup`, or bare `OpCleanup` — which only NewCleanupOp
// may do.
func stampsOpCleanup(cl *ast.CompositeLit) bool {
	switch v := keyedField(cl, "Kind").(type) {
	case *ast.SelectorExpr:
		x, ok := v.X.(*ast.Ident)
		return ok && x.Name == "adapter" && v.Sel.Name == "OpCleanup"
	case *ast.Ident:
		return v.Name == "OpCleanup"
	}
	return false
}

// hasCleanupShape reports whether a FileOp literal is statically the cleanup
// shape: an empty-object Content headed for a key-merge destination, i.e. it
// also names a MergeStrategy other than the literal "replace", or OwnedKeys. A
// whole-file write of "{}" names neither and is not the shape.
func hasCleanupShape(cl *ast.CompositeLit) bool {
	if !hasEmptyObjectContent(cl) {
		return false
	}
	if keyedField(cl, "OwnedKeys") != nil {
		return true
	}
	strat := keyedField(cl, "MergeStrategy")
	if strat == nil {
		return false
	}
	if bl, ok := strat.(*ast.BasicLit); ok && bl.Kind == token.STRING {
		// A parser-produced string literal always unquotes; if one ever did
		// not, it is treated like a non-literal strategy — not provably
		// "replace", so still the shape.
		s, err := strconv.Unquote(bl.Value)
		return err != nil || s != "replace"
	}
	return true
}

// hasEmptyObjectContent reports whether a FileOp literal sets Content to the
// static empty object — `[]byte("{}")` or `[]byte(`{}`)`, ignoring all
// whitespace, like the merge path does. Content built from a variable or a
// call, or assigned after construction, is not a static shape and is not
// matched (nor is a string literal that fails to unquote, which the parser
// never produces); the guard is deliberately literal-only, like its Skip
// sibling.
func hasEmptyObjectContent(cl *ast.CompositeLit) bool {
	call, ok := keyedField(cl, "Content").(*ast.CallExpr)
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
	return err == nil && strings.Join(strings.Fields(s), "") == "{}"
}
