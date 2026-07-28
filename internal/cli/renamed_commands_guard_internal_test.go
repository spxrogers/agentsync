package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNoStaleRenamedCommandReferences pins the user-facing text against the
// HARD renames and removals shipped in #200 (F2/F4/F8).
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

	// old invocation -> what to write instead. Matched on a WORD boundary (see
	// containsInvocation): `update` and `install` are ordinary English words, so
	// a bare substring test would flag prose like "agentsync updates the native
	// config" and hand the next author a bogus failure whose cheapest fix is to
	// add their file to `exempt` — permanently un-guarding it.
	renamed := map[string]string{
		"agentsync secrets":        "agentsync secret …",
		"agentsync verify":         "agentsync check",
		"agentsync plugin install": "agentsync plugin add",
		"agentsync update":         "agentsync plugin outdated / agentsync plugin upgrade --all",
	}

	// Files that exist precisely to say "the old spelling is gone", so naming it
	// is their job rather than a stale hint:
	//   - upgrade_notice.go is the first-run-after-upgrade banner, whose entire
	//     content is old-name → new-name lines.
	//   - upgrading.mdx and CHANGELOG.md document the renames themselves.
	//
	// (The negative acceptance tests naming the old spellings need no entry:
	// _test.go files are never scanned.)
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
				if containsInvocation(line, old) {
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
	// …AND the config/data files that carry user-facing command lists but are
	// neither Go nor markdown. Three of them (context7.json, llms.txt, the
	// .golangci.yml comment) held stale spellings that this guard structurally
	// could not see, and had to be found by hand.
	if err := walkRepoLooseFiles(repoRoot, scan); err != nil {
		t.Fatalf("walk loose files: %v", err)
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

// containsInvocation reports whether line names the retired invocation `old` as
// a COMMAND rather than incidentally as prose.
//
// The match is anchored on a word boundary at the end. Every key ends in a verb
// that is also an ordinary English word — `update`, `install`, `verify`,
// `secrets` — so a plain strings.Contains flags "agentsync updates the native
// config in place" as if it were a stale command reference. That failure is
// worse than a miss: it lands on an author who did nothing wrong, and the
// cheapest way out is to add the file to `exempt`, which un-guards it forever.
//
// Only the trailing edge needs anchoring. The leading "agentsync " prefix is
// already a boundary in practice, and requiring one before it would miss the
// common case of the invocation being backtick-quoted in markdown.
func containsInvocation(line, old string) bool {
	for i := 0; ; {
		j := strings.Index(line[i:], old)
		if j < 0 {
			return false
		}
		end := i + j + len(old)
		if end == len(line) || !isWordByte(line[end]) {
			return true
		}
		i = end
	}
}

// isWordByte reports whether b continues a command word. Only ASCII letters
// count: `agentsync verify-ish` is prose, but `agentsync verify --json`,
// `agentsync verify`, and `agentsync verify.` are all the command.
func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// walkRepoLooseFiles visits the handful of non-Go, non-markdown files that
// carry user-facing command lists: the LLM-facing summaries, the lint config's
// prose comments, and the CI workflows. Each is named explicitly rather than
// globbed by extension — a blanket *.json/*.yml sweep would pull in lockfiles,
// generated output, and vendored config, none of which describe the CLI.
//
// A named file that does not exist is skipped, not an error: entries may name
// files a later change adds.
func walkRepoLooseFiles(root string, fn func(rel, src string)) error {
	rels := []string{
		"context7.json",
		"website/public/llms.txt",
		".golangci.yml",
		"justfile",
	}
	wf := filepath.Join(root, ".github", "workflows")
	if entries, err := os.ReadDir(wf); err == nil {
		for _, e := range entries {
			if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
				rels = append(rels, filepath.ToSlash(filepath.Join(".github", "workflows", e.Name())))
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		fn(rel, string(data))
	}
	return nil
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
