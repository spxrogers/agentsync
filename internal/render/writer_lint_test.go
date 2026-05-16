package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDirectAtomicWriteOutsideAllowedFiles is a belt-and-braces
// complement to the forbidigo lint rule: it walks the repo and fails the
// build if any non-test, non-allowlisted file calls iox.AtomicWrite
// directly. The forbidigo rule in .golangci.yml gates CI; this test
// gates `go test` locally so a regression on a developer machine is
// visible before push, even if their lint binary version skew is a
// problem.
//
// The allowlist matches the forbidigo exclusion list. If you need to
// add a path, the bar is: "this write does NOT target a file the user
// could plausibly have hand-managed before agentsync was installed."
// Native agent destinations (~/.claude*, ~/.config/opencode/*, etc.)
// always go through render.Writer / adapter.DestWriter.
func TestNoDirectAtomicWriteOutsideAllowedFiles(t *testing.T) {
	repoRoot := findRepoRoot(t)

	// Files allowed to call iox.AtomicWrite directly. These write to
	// canonical source (~/.agentsync/*), agentsync's own state, or the
	// plugin cache — none of which are native destinations.
	allowed := map[string]bool{
		"internal/iox/atomic.go":         true,
		"internal/render/writer.go":      true,
		"internal/source/writer.go":      true,
		"internal/state/store.go":        true,
		"internal/cli/plugin.go":         true,
		"internal/cli/marketplace.go":    true,
		"internal/cli/agent.go":          true,
		"internal/cli/reconcile.go":      true,
		"internal/cli/update.go":         true,
		"internal/adapter/testwriter.go": true,
	}

	var bad []string
	walkErr := filepathWalk(repoRoot, func(path string) error {
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // tests can use whatever they need
		}
		rel, _ := filepath.Rel(repoRoot, path)
		// Normalize separators for the allowlist key.
		rel = filepath.ToSlash(rel)
		if allowed[rel] {
			return nil
		}
		// Skip vendored / generated trees.
		if strings.HasPrefix(rel, "vendor/") || strings.HasPrefix(rel, ".git/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Match the call form (open paren after the name) so we don't
		// trip on doc-comment references like "see iox.AtomicWrite for…".
		if containsCallSite(string(data), "iox.AtomicWrite") {
			bad = append(bad, rel)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
	if len(bad) > 0 {
		t.Fatalf("direct iox.AtomicWrite calls found in non-allowlisted files — adapters and any code targeting native destinations must route through render.Writer / adapter.DestWriter:\n  %s\nIf this write is to canonical source / state / plugin-cache, add it to the allowlist in writer_lint_test.go AND .golangci.yml exclusions.", strings.Join(bad, "\n  "))
	}
}

// findRepoRoot walks up from this test file's package until it finds go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod walking up from test cwd")
		}
		dir = parent
	}
}

// containsCallSite reports whether src contains a call-form occurrence
// of name (i.e. "<name>(" with possibly whitespace between the name and
// the paren). This filters out doc-comment mentions.
func containsCallSite(src, name string) bool {
	rest := src
	for {
		i := strings.Index(rest, name)
		if i < 0 {
			return false
		}
		j := i + len(name)
		// Skip any whitespace between the name and what follows.
		for j < len(rest) && (rest[j] == ' ' || rest[j] == '\t') {
			j++
		}
		if j < len(rest) && rest[j] == '(' {
			return true
		}
		rest = rest[i+len(name):]
	}
}

// filepathWalk is a minimal local walker that visits every regular file
// under root. We don't import path/filepath.WalkDir to keep the test's
// import surface tight; the recursive call is fine for our small tree.
func filepathWalk(root string, fn func(string) error) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		if e.IsDir() {
			if e.Name() == ".git" || e.Name() == "vendor" || e.Name() == "node_modules" {
				continue
			}
			if err := filepathWalk(p, fn); err != nil {
				return err
			}
			continue
		}
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}
