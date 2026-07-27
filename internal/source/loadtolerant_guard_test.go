package source_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLoadTolerantCallersAreSanctioned pins the caller set of
// source.LoadTolerant, the single documented bypass of the fail-closed
// agents/ → subagents/ layout gate (#200 F1).
//
// Why a guard and not a doc line: the gate exists because a tolerant load of an
// unmigrated tree reports ZERO subagents, and render.PruneStaleState reads "no
// longer rendered" as "source component removed" — so one `apply` behind a
// tolerant load disowns every subagent the user had rendered. Nothing in the
// type system distinguishes a read-only diagnostic from a path that reaches
// apply, so a new LoadTolerant call is invisible in review and silent at
// runtime. It shipped once already: the component-list groups were added as a
// second caller in the same PR that documented "exists for one caller".
//
// The bar for adding a file here: the caller is READ-ONLY, cannot reach
// apply/PruneStaleState, and either reports the pending migration itself or
// does not read c.Subagents at all.
func TestLoadTolerantCallersAreSanctioned(t *testing.T) {
	repoRoot := findRepoRootForGuard(t)

	sanctioned := map[string]string{
		"internal/source/loader.go": "the definition itself (Load delegates to it after gating)",
		"internal/cli/doctor.go":    "surfaces the pending migration as a failing check; must not abort at load",
		"internal/cli/component_list.go": "read-only listings; the subagent lister gates on " +
			"CheckSubagentLayout itself via requiresMigratedLayout",
	}

	var unexpected []string
	walkErr := walkGoFiles(repoRoot, func(rel, src string) {
		if _, ok := sanctioned[rel]; ok {
			return
		}
		if strings.Contains(src, "LoadTolerant(") {
			unexpected = append(unexpected, rel)
		}
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
	if len(unexpected) > 0 {
		t.Fatalf("unsanctioned source.LoadTolerant callers:\n  %s\n\n"+
			"LoadTolerant SKIPS the agents/ → subagents/ layout gate, so it reports zero subagents on an "+
			"unmigrated tree. If this caller can reach apply/PruneStaleState, that silently disowns every "+
			"rendered subagent — use source.Load instead.\nIf the caller is genuinely read-only and either "+
			"reports the pending migration itself or never reads c.Subagents, add it to the sanctioned map "+
			"here with the reason.", strings.Join(unexpected, "\n  "))
	}

	// A guard whose allowlist has gone stale is worse than no guard: it reads as
	// coverage while pinning nothing. Fail if a sanctioned entry no longer calls
	// LoadTolerant at all.
	for rel := range sanctioned {
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("sanctioned file %s is unreadable (moved or deleted?): %v", rel, err)
		}
		if !strings.Contains(string(data), "LoadTolerant(") {
			t.Errorf("%s is sanctioned for source.LoadTolerant but no longer calls it — drop it from the map", rel)
		}
	}
}

// walkGoFiles visits every non-test .go file under root with its repo-relative
// slash path and contents.
func walkGoFiles(root string, fn func(rel, src string)) error {
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				switch e.Name() {
				case ".git", "vendor", "node_modules", "website":
					continue
				}
				if err := walk(p); err != nil {
					return err
				}
				continue
			}
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			fn(filepath.ToSlash(rel), string(data))
		}
		return nil
	}
	return walk(root)
}

// findRepoRootForGuard locates the repo root relative to THIS source file, not
// the process cwd: `go test` shares one process per package, so a sibling test
// that chdirs would otherwise silently redirect this walk.
func findRepoRootForGuard(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod walking up from " + thisFile)
		}
		dir = parent
	}
}
