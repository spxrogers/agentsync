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

	// old invocation -> what to write instead, derived from the single
	// `retirements` table (retirements_internal_test.go) rather than restated
	// here. A hand-maintained third copy is how this guard goes blind: add a
	// retirement to the resurrection guard's list only, and the prose scan
	// keeps passing while every doc still names the dead command.
	//
	// Only the FailsAsUnknown rows belong here. `explain <plugin>` is excluded
	// deliberately — `agentsync explain` still resolves, so a doc naming it is
	// not handing the user a broken command, and scanning for it would flag the
	// legitimate `explain <path>` prose this release added.
	//
	// Matching is word-boundary aware; see containsInvocation for why.
	renamed := map[string]string{}
	for _, r := range retirements {
		if !r.FailsAsUnknown {
			continue
		}
		var to []string
		for _, n := range r.Replacements {
			to = append(to, "agentsync "+n)
		}
		renamed["agentsync "+r.Old] = strings.Join(to, " / ")
	}
	if len(renamed) == 0 {
		t.Fatal("no retirements fail as unknown commands — this guard would vacuously pass")
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
	// An empty needle matches at every index and advances `i` by zero, so the
	// loop below would spin forever — turning a typo'd table entry into a CI
	// timeout rather than a failure. Nothing is ever a stale reference to "".
	if old == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(line[i:], old)
		if j < 0 {
			return false
		}
		end := i + j + len(old)
		if end == len(line) || !isWordByte(line[end]) {
			return true
		}
		// Advance one past the START of this occurrence, not past its end: a
		// boundary-satisfying match can begin INSIDE the one just rejected when
		// the needle overlaps itself. No current key does (they all begin
		// "agentsync "), but the correctness of this loop should not rest on
		// that. containsInvocation("aaa", "aa") is the minimal case.
		i += j + 1
	}
}

// isWordByte reports whether b continues a command word: ASCII letters, digits
// and hyphen — the characters a command or flag name is spelled from.
//
// It has to do two jobs. Ending the word at a non-word byte is what separates
// `agentsync update --apply` (an invocation) from `agentsync updates the native
// config` (prose) — the false-positive class this matcher exists to prevent.
// Including the hyphen is what stops a longer FLAG from satisfying a shorter
// one: without it, `agentsync plugin upgrade --all-agents` would count as
// naming `agentsync plugin upgrade --all`, which the notice guard relies on.
//
// Underscore is deliberately NOT in the class, though it is a legal identifier
// byte: markdown writes emphasis as _agentsync update_, and this repo uses that
// form, so including it made a real stale reference invisible. `update_all` is
// not a command anyone writes; _agentsync update_ is a sentence someone does.
//
// A non-ASCII continuation (`agentsync verifyé`) reads as a boundary and is
// therefore matched. That is the safe direction — a false positive is a
// reviewer conversation, a false negative is a broken command left in a doc.
func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '-'
}

// TestContainsInvocation exercises the matcher directly.
//
// It needs its own test because the repo scan cannot serve as one. The scan
// currently has ZERO hits — that is the passing state — so a matcher that
// always returned false would look identical. The exempt-staleness loop in
// TestNoStaleRenamedCommandReferences catches a TOTAL failure, but it is a
// per-file OR across every key: break the match for one key only (the newest
// one, always the least protected) and the surviving keys keep the exempt files
// hit, so the suite stays green while that retirement goes unguarded..
func TestContainsInvocation(t *testing.T) {
	cases := []struct {
		name string
		line string
		old  string
		want bool
	}{
		{"bare invocation", "run agentsync update to poll", "agentsync update", true},
		{"backtick-quoted", "run `agentsync update` nightly", "agentsync update", true},
		{"with a flag", "agentsync update --apply", "agentsync update", true},
		{"end of line", "just run agentsync update", "agentsync update", true},
		{"trailing period", "use agentsync verify.", "agentsync verify", true},
		{"in a markdown table cell", "| `agentsync verify` | `agentsync check` |", "agentsync verify", true},

		// The false-positive class this matcher exists to prevent: every key
		// ends in a word that is also ordinary English.
		{"prose plural", "agentsync updates the native config in place", "agentsync update", false},
		{"prose third person", "agentsync verifies every reference", "agentsync verify", false},
		{"prose gerund", "agentsync installs nothing into the agent", "agentsync install", false},

		// `secret` is a strict prefix of `secrets`, so the boundary is what
		// keeps the two retirements from matching each other's lines.
		{"prefix of a longer command", "agentsync secrets set FOO", "agentsync secret", false},
		{"the longer command itself", "agentsync secrets set FOO", "agentsync secrets", true},

		// A prose occurrence must not mask a real one later on the same line.
		{"prose then real", "agentsync updates things; run agentsync update --apply", "agentsync update", true},
		{"real then prose", "run agentsync update, since agentsync updates things", "agentsync update", true},

		{"absent", "nothing to see here", "agentsync update", false},

		// A command word continues through digits, hyphens and underscores, so
		// none of these is the retired command.
		{"hyphenated", "agentsync verify-ish", "agentsync verify", false},
		{"digit suffix", "agentsync update2", "agentsync update", false},
		// Markdown emphasis must still be caught: underscore is not a word byte.
		{"markdown italics", "see _agentsync update_ for the nightly refresh", "agentsync update", true},
		// The reason the hyphen is in the class: a longer flag must not satisfy
		// a shorter one, which the upgrade-notice guard depends on.
		{"longer flag", "run `agentsync plugin upgrade --all-agents`", "agentsync plugin upgrade --all", false},
		{"exact flag", "run `agentsync plugin upgrade --all`", "agentsync plugin upgrade --all", true},
		// A needle carrying a placeholder, as the `explain <plugin>` row does.
		{"bracket needle", "`agentsync explain <plugin>` moved", "agentsync explain <plugin>", true},
		// Self-overlapping needle: the boundary-satisfying match starts inside
		// the rejected one. No real key overlaps itself; the loop must still
		// find it.
		{"self-overlapping needle", "aaa", "aa", true},

		// A typo'd table entry must fail, not hang. Without the guard clause
		// this case spins forever and the whole package times out.
		{"empty needle", "agentsync verify", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsInvocation(tc.line, tc.old); got != tc.want {
				t.Errorf("containsInvocation(%q, %q) = %v, want %v", tc.line, tc.old, got, tc.want)
			}
		})
	}
}

// walkRepoLooseFiles visits the files that carry user-facing command text but
// are not reached by walkRepoGoFiles or walkRepoDocs: the LLM-facing summaries,
// the agent-memory files (which enumerate the registered command tree, so every
// rename must land in them), the lint config's prose comments, the release and
// issue-template config, the CI workflows, and the shell scripts.
//
// Named explicitly rather than globbed by extension. A blanket *.json/*.yml
// sweep would pull in lockfiles, generated output, and vendored config — none
// of which describe the CLI, all of which are noise the first time one happens
// to contain the word "update".
//
// A named file that does not exist is skipped, not an error: entries may name
// files a later change adds. CLAUDE.md and AGENTS.md are rendered from
// .agentsync/memory/AGENTS.md, so all three are listed — a stale spelling
// should be reported at the canonical source AND at the build output, since
// fixing only the render is undone by the next `agentsync apply`.
func walkRepoLooseFiles(root string, fn func(rel, src string)) error {
	rels := []string{
		"context7.json",
		"website/public/llms.txt",
		".golangci.yml",
		"justfile",
		".goreleaser.yaml",
		"CLAUDE.md",
		"AGENTS.md",
		".agentsync/memory/AGENTS.md",
	}
	for _, dir := range []string{
		filepath.Join(".github", "workflows"),
		filepath.Join(".github", "ISSUE_TEMPLATE"),
		"scripts",
	} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			switch {
			case strings.HasSuffix(e.Name(), ".yml"),
				strings.HasSuffix(e.Name(), ".yaml"),
				strings.HasSuffix(e.Name(), ".sh"):
				rels = append(rels, filepath.ToSlash(filepath.Join(dir, e.Name())))
			}
		}
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

// generatedWebsiteDocs are rendered from docs/*.md by website/scripts/sync-docs.mjs
// at build time and gitignored. The list mirrors website/.gitignore.
var generatedWebsiteDocs = map[string]bool{
	"website/src/content/docs/concepts/index.md":              true,
	"website/src/content/docs/internals/architecture.md":      true,
	"website/src/content/docs/internals/components.md":        true,
	"website/src/content/docs/reference/capability-matrix.md": true,
	"website/src/content/docs/comparison/index.md":            true,
}

// walkRepoDocs visits the user-facing prose: docs/*.md, the authored website
// pages, the repo's own .agentsync/skills/, test/ (its README and the .feature
// files, whose assertions are behavior locks that can go green on a stale
// string), and the root README/CHANGELOG/SECURITY/CONTRIBUTING.
//
// The generated website copies are skipped by exact path (generatedWebsiteDocs).
// They are rendered from docs/*.md at build time and gitignored, so a stale
// line in one is really a stale line in docs/ — reporting both sends the reader
// to fix the build output..
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
			rel, _ := filepath.Rel(root, p)
			slash := filepath.ToSlash(rel)
			if generatedWebsiteDocs[slash] {
				continue
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fn(slash, string(data))
		}
		return nil
	}
	for _, sub := range []string{"docs", "website/src/content/docs", "test", ".agentsync/skills"} {
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
