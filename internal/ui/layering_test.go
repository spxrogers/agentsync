package ui_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAdapterAndCaptureDoNotImportUI enforces the layering rule that the
// `warning: ` sentinel exists to satisfy.
//
// `internal/adapter/*` and `internal/capture` must not depend on `internal/ui`.
// That is why an adapter emits a plain `warning: <message>` line into an
// `io.Writer` it was handed, and why `ui.WarnWriter` — not the adapter — attaches
// the label and sanitizes the body. `docs/architecture.md` §11–§12 state the rule,
// and §12's mermaid graph draws `ui` as its own node specifically so that an
// `adapter → INFRA` edge cannot imply `adapter → ui`.
//
// Until this test existed, nothing checked it. A stated architectural rule with no
// enforcement is a convention: the next person to want a styled warning inside an
// adapter would add the import, every test would pass, and the sentinel would
// quietly become dead weight. This is deliberately a source-level import check
// rather than a lint rule so it lives next to the package it protects.
func TestAdapterAndCaptureDoNotImportUI(t *testing.T) {
	const forbidden = "github.com/spxrogers/agentsync/internal/ui"
	root := repoRootForLayering(t)

	dirs, err := filepath.Glob(filepath.Join(root, "internal", "adapter", "*"))
	if err != nil {
		t.Fatal(err)
	}
	dirs = append(
		dirs,
		filepath.Join(root, "internal", "adapter"),
		filepath.Join(root, "internal", "capture"),
	)

	fset := token.NewFileSet()
	checked := 0
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			// Test files may import ui (a test asserting on styled output is fine);
			// the constraint is on the packages themselves.
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			checked++
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, imp := range f.Imports {
				if strings.Trim(imp.Path.Value, `"`) == forbidden {
					t.Errorf("%s imports internal/ui — adapters and capture must not; "+
						"emit a plain \"warning: \" line into the io.Writer you were handed and let "+
						"ui.WarnWriter label it (see docs/architecture.md §11)",
						fset.Position(imp.Pos()))
				}
			}
		}
	}
	// The adapter tree is large; a glob that silently stops matching would make
	// this pass vacuously — the failure mode two other guards in this repo hit.
	if checked < 40 {
		t.Fatalf("only checked %d adapter/capture sources; the sweep has lost its targets", checked)
	}
}

func repoRootForLayering(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate this test file")
	}
	// <root>/internal/ui/layering_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(self)))
}
