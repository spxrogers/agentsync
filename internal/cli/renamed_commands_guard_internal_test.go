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

	// Files that exist precisely to say "the old spelling is gone", so naming it
	// is their job rather than a stale hint:
	//   - upgrade_notice.go is the first-run-after-upgrade banner, whose entire
	//     content is old-name → new-name lines.
	//   - upgrading.mdx and CHANGELOG.md document the renames themselves.
	//   - the negative acceptance tests assert the old spellings no longer
	//     resolve, so they must name them.
	exempt := map[string]bool{
		"internal/cli/upgrade_notice.go":                   true,
		"website/src/content/docs/reference/upgrading.mdx": true,
		"CHANGELOG.md": true,
	}

	type hit struct{ file, old, line string }
	var hits []hit
	seenExempt := map[string]bool{}

	scan := func(rel, src string) {
		hitHere := false
		for _, line := range strings.Split(src, "\n") {
			for old := range renamed {
				if strings.Contains(line, old) {
					hitHere = true
					if !exempt[rel] {
						hits = append(hits, hit{rel, old, strings.TrimSpace(line)})
					}
				}
			}
		}
		if hitHere && exempt[rel] {
			seenExempt[rel] = true
		}
	}

	// Go sources…
	if err := walkRepoGoFiles(repoRoot, scan); err != nil {
		t.Fatalf("walk go files: %v", err)
	}
	// …AND the prose. Round 1's BLOCKER in this class lived in docs/user-guide.md
	// and the website command reference, not in Go at all — a guard that reads
	// only .go files would have missed the very bug that motivated it.
	if err := walkRepoDocs(repoRoot, scan); err != nil {
		t.Fatalf("walk docs: %v", err)
	}

	for _, h := range hits {
		t.Errorf("%s references the hard-renamed `%s` — there is NO alias, so this hint errors as an "+
			"unknown command for the user. Write `%s` instead.\n    %s",
			h.file, strings.TrimSpace(h.old), renamed[h.old], h.line)
	}

	// An allowlist that has gone stale reads as coverage while pinning nothing.
	// A file that does not exist is not stale, though: some exempt entries name
	// files that arrive in a later change, and a guard that failed for their
	// absence would block the branch that adds them.
	for rel := range exempt {
		if seenExempt[rel] {
			continue
		}
		if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(rel))); os.IsNotExist(err) {
			continue
		}
		t.Errorf("%s is exempted from the renamed-command check but no longer mentions any "+
			"renamed command — drop it from the exempt map", rel)
	}
}

// walkRepoDocs visits the user-facing prose: docs/*.md, the authored website
// pages, test/ (its README and the .feature files, whose assertions are
// behavior locks that can go green on a stale string), and the root
// README/CHANGELOG/SECURITY/CONTRIBUTING. The generated website copies under
// website/src/content/docs/{concepts,architecture,components,reference/capability-matrix}
// are excluded — they are produced from docs/*.md at build time and are
// gitignored, so flagging them would report the same line twice.
func walkRepoDocs(root string, fn func(rel, src string)) error {
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				switch e.Name() {
				case "node_modules", "dist", ".astro":
					continue
				case "superpowers":
					// docs/superpowers/{specs,plans} are dated design records of
					// what was built when. They describe the surface as it stood
					// at the time, so "correcting" them would falsify the
					// archive; they are not guidance a user follows.
					continue
				}
				if err := walk(p); err != nil {
					return err
				}
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".mdx") &&
				!strings.HasSuffix(name, ".feature") {
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
	for _, sub := range []string{"docs", "website/src/content/docs", "test"} {
		if err := walk(filepath.Join(root, filepath.FromSlash(sub))); err != nil {
			return err
		}
	}
	for _, f := range []string{"README.md", "CHANGELOG.md", "SECURITY.md", "CONTRIBUTING.md"} {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		fn(f, string(data))
	}
	return nil
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

// readFileForGuard reads a repo-relative file for a guard test.
func readFileForGuard(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}
