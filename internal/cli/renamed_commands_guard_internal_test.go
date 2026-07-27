package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNoStaleRenamedCommandReferences pins the user-facing text against the
// HARD renames shipped in #200 (F2/F4/F8).
//
// These renames have no aliases by explicit decision, so a hint that still says
// "run `agentsync secrets set …`" is not merely stale — it hands the user a
// command that errors as unknown, at the exact moment they are already stuck
// (an unwritable vault, a missing age file, a refused capture). Five such hints
// shipped in the rename commit itself, across three packages, because nothing
// was checking.
//
// The check is textual and deliberately narrow: it matches the old spelling
// only where it is written as an `agentsync <verb>` invocation, so prose about
// "secrets handling" or a `secrets.Resolved` type name is unaffected.
func TestNoStaleRenamedCommandReferences(t *testing.T) {
	repoRoot := repoRootFromCaller(t)

	// old invocation -> what to write instead.
	renamed := map[string]string{
		"agentsync secrets ":       "agentsync secret …",
		"agentsync verify":         "agentsync check",
		"agentsync plugin install": "agentsync plugin add",
	}

	// update.go IS the deprecation shim: naming the old spelling is its job.
	exempt := map[string]bool{
		"internal/cli/update.go": true,
	}

	type hit struct{ file, old, line string }
	var hits []hit

	walkErr := walkRepoGoFiles(repoRoot, func(rel, src string) {
		if exempt[rel] {
			return
		}
		for _, line := range strings.Split(src, "\n") {
			for old := range renamed {
				if strings.Contains(line, old) {
					hits = append(hits, hit{rel, old, strings.TrimSpace(line)})
				}
			}
		}
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}

	for _, h := range hits {
		t.Errorf("%s references the hard-renamed `%s` — there is NO alias, so this hint errors as an "+
			"unknown command for the user. Write `%s` instead.\n    %s",
			h.file, strings.TrimSpace(h.old), renamed[h.old], h.line)
	}
}

// walkRepoGoFiles visits every non-test .go file under root with its
// repo-relative slash path and contents.
func walkRepoGoFiles(root string, fn func(rel, src string)) error {
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

// repoRootFromCaller locates the repo root relative to THIS source file rather
// than the process cwd — other tests in this package chdir into temp trees, and
// `go test` shares one process per package.
func repoRootFromCaller(t *testing.T) string {
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
