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
		"internal/iox/atomic.go":           true,
		"internal/render/writer.go":        true,
		"internal/source/writer.go":        true,
		"internal/state/store.go":          true,
		"internal/secrets/age.go":          true, // writes secrets.age under ~/.agentsync/
		"internal/cli/plugin.go":           true,
		"internal/cli/marketplace.go":      true,
		"internal/cli/agent.go":            true,
		"internal/cli/gitbackup_config.go": true, // writes agentsync.toml (canonical source)
		"internal/cli/reconcile.go":        true,
		"internal/cli/update.go":           true,
		"internal/adapter/testwriter.go":   true,
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

// TestNoDirectDestructiveOSCallsOutsideAllowedFiles is the belt-and-braces
// complement to the os.* forbidigo rules (issue #163): it fails the build if any
// non-test, non-allowlisted file calls a destructive os.* write primitive
// directly. Those bypass the DestWriter foreign-collision backup invariant
// exactly like a direct iox.AtomicWrite. It covers the create/overwrite/delete
// AND move/truncate primitives — os.Remove/os.RemoveAll/os.WriteFile/os.Create
// plus os.OpenFile (the O_TRUNC truncate-write vector), os.Rename, and
// os.Truncate — so an adapter cannot overwrite a pre-existing native file by
// reaching for one of the less-obvious calls. (os.OpenFile also matches a
// read-only open; that is deliberate over-approximation — a raw open of a native
// destination path, even to read, should be reviewed. Allowlist a genuine
// non-destination read-open with a comment.)
//
// The allowlist here is PER-FILE, so it is coarser than the .golangci.yml os.*
// fence for the internal/cli files, where the lint exclusions are line-scoped
// `//nolint:forbidigo // <reason>` comments at each call site (only
// internal/cli/update.go keeps a whole-file lint exclude); the whole-dir lint
// excludes (internal/iox, internal/git, internal/marketplace) are conversely
// coarser than this list. Keep BOTH in sync when adding a legitimate
// non-destination os.* site: a new site in an already-listed CLI file still
// needs its own line-scoped nolint to pass lint.
func TestNoDirectDestructiveOSCallsOutsideAllowedFiles(t *testing.T) {
	repoRoot := findRepoRoot(t)

	// Files allowed direct os.* — the DestWriter itself plus non-destination
	// writers: canonical source, the vault, plugin/marketplace cache, git-backup
	// repo scaffolding, iox internals, and a writability probe. One deliberate
	// NATIVE-destination exception rides along: reconcile.go's interactive
	// orphan removal os.Remove's a native dest file, safe only because
	// render.BackupFile runs first (see its line-scoped nolint).
	allowed := map[string]bool{
		"internal/render/writer.go":              true, // the DestWriter — owns delete + backup
		"internal/source/writer.go":              true, // canonical-source writer; WriteSkill prunes orphaned bundled files (os.Remove)
		"internal/adapter/testwriter.go":         true, // PassThroughWriter test helper
		"internal/iox/atomic.go":                 true,
		"internal/git/init.go":                   true, // local git-backup repo scaffolding
		"internal/marketplace/fetch_git.go":      true, // fetch scratch dirs + swap-in rename
		"internal/marketplace/fetch_relative.go": true, // copies fetched files into the cache
		"internal/marketplace/fetch_npm.go":      true, // unpacks the npm tarball into the cache
		"internal/cli/init.go":                   true, // scaffolds ~/.agentsync (canonical source)
		"internal/cli/migrate.go":                true, // moves the canonical agents/ dir to subagents/ (canonical source)
		"internal/cli/secrets.go":                true, // vault rollback + cleartext temp cleanup
		"internal/cli/marketplace.go":            true, // canonical source + marketplace cache
		"internal/cli/plugin.go":                 true, // canonical source + plugin cache
		"internal/cli/update.go":                 true, // marketplace/plugin cache scratch + swap-in rename
		"internal/cli/mcp.go":                    true, // removes mcp/<id>.toml (canonical source)
		"internal/cli/doctor.go":                 true, // ~/.agentsync writability probe
		"internal/cli/reconcile.go":              true, // canonical-source write-back + the backed-up native orphan removal
	}
	forbidden := []string{"os.Remove", "os.RemoveAll", "os.WriteFile", "os.Create", "os.OpenFile", "os.Rename", "os.Truncate"}

	var bad []string
	walkErr := filepathWalk(repoRoot, func(path string) error {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		rel = filepath.ToSlash(rel)
		// test/ is BDD/e2e harness code writing fixtures to temp dirs (via
		// AGENTSYNC_TARGET_ROOT), never a native destination — treat it like _test.go.
		if allowed[rel] || strings.HasPrefix(rel, "vendor/") || strings.HasPrefix(rel, ".git/") || strings.HasPrefix(rel, "test/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(data)
		for _, name := range forbidden {
			if containsCallSite(src, name) {
				bad = append(bad, rel+" ("+name+")")
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
	if len(bad) > 0 {
		t.Fatalf("direct destructive os.* (Remove/RemoveAll/WriteFile/Create/OpenFile/Rename/Truncate) in non-allowlisted files — writes to native destinations must route through render.Writer / adapter.DestWriter:\n  %s\nIf this write is to canonical source / state / cache / git-backup (or a genuine non-destination read-open), add it to the allowlist here AND the .golangci.yml exclusions.", strings.Join(bad, "\n  "))
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
