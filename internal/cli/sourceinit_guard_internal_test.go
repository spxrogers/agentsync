package cli

import (
	"sort"
	"strings"
	"testing"
)

// statsAgentsyncTOML reports whether src contains a stat-shaped
// initialized-source probe: an `os.Stat(` and the literal "agentsync.toml" on
// ONE line. Factored out of the repo walk so the guard can be exercised against
// a synthetic source — see the negative-control subtest.
func statsAgentsyncTOML(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "os.Stat(") && strings.Contains(line, `"agentsync.toml"`) {
			return true
		}
	}
	return false
}

// scanProbeViolations applies statsAgentsyncTOML across files, returning the
// non-allowlisted matches (sorted, so a failure message is stable) and the set
// of allowlisted files that still match. files maps a repo-relative slash path
// to its source.
func scanProbeViolations(files map[string]string, allowed map[string]string) (unexpected []string, seen map[string]bool) {
	seen = map[string]bool{}
	for rel, src := range files {
		if !statsAgentsyncTOML(src) {
			continue
		}
		if _, ok := allowed[rel]; ok {
			seen[rel] = true
			continue
		}
		unexpected = append(unexpected, rel)
	}
	sort.Strings(unexpected)
	return unexpected, seen
}

// TestInitializedSourceProbeIsInOnePlace pins that the STAT-SHAPED "is this
// source root initialized" probe has one definition.
//
// Three copies of that shape existed before #228 — check's
// requireInitializedSource, doctor's checkHomeDir, and the upgrade notice's
// hasUserConfig — and they tested three different things, so the same broken
// tree got three different answers. The tell was always the same line shape: an
// os.Stat of a path joined with "agentsync.toml".
//
// WHAT THIS GUARD ENFORCES, EXACTLY: no production Go file outside
// internal/cli/sourceinit.go contains `os.Stat(` and `"agentsync.toml"` on one
// line. That is narrower than "there is only one initialized-source probe in
// the codebase", and deliberately so:
//   - KNOWN EXCEPTION, not a violation: loadSecretsConfig
//     (internal/cli/secrets.go) also answers "is this home initialized", via
//     os.ReadFile + an ENOENT arm carrying its own "run `agentsync init`"
//     message. It reads the file because it must PARSE it; the initialization
//     answer is a by-product of that read, not a separate stat. It is untouched
//     by #228 and does not disagree with probeSourceInit about whether a tree is
//     usable: that read also fails on a missing root, a root that is a regular
//     file, a missing agentsync.toml and a directory-shaped agentsync.toml —
//     only the wording of the refusal differs. Folding it in would mean stat-ing
//     a file it is about to read. It does not match the pattern above, and it is
//     not meant to.
//   - LIMIT: the pattern is line-shaped, so a probe written with os.Lstat, an
//     afero FS, os.ReadFile, or with the filename held in a variable slips
//     past. It catches the copy-paste that actually happened three times, not
//     every conceivable spelling. Widening it to a read-shaped pattern was
//     considered and declined: os.ReadFile of a config path is a shape the rest
//     of the codebase uses legitimately, so the guard would become a
//     false-positive generator.
//   - LIMIT: walkRepoGoFiles skips _test.go files, so a test that stats
//     agentsync.toml to assert on a tree it built (init_test.go does) is out of
//     scope. Tests observe the filesystem; they do not define what
//     "initialized" means.
func TestInitializedSourceProbeIsInOnePlace(t *testing.T) {
	repoRoot := repoRootFromCaller(t)

	allowed := map[string]string{
		"internal/cli/sourceinit.go": "probeSourceInit — the one stat-shaped initialized-source probe",
	}

	files := map[string]string{}
	if err := walkRepoGoFiles(repoRoot, func(rel, src string) { files[rel] = src }); err != nil {
		t.Fatalf("walk: %v", err)
	}

	unexpected, seen := scanProbeViolations(files, allowed)
	if len(unexpected) > 0 {
		t.Errorf("a stat-shaped initialized-source probe outside internal/cli/sourceinit.go:\n  %s\n\n"+
			"Three copies of this probe disagreed about what \"initialized\" means (#228). "+
			"Call probeSourceInit and render its state instead of stat-ing agentsync.toml here.",
			strings.Join(unexpected, "\n  "))
	}
	// An allowlist that has gone stale is worse than no guard: it reads as
	// coverage while pinning nothing. This arm doubles as proof-of-life for the
	// repo walk — if the walk ever returns nothing, the guard fails here rather
	// than passing vacuously.
	for rel, reason := range allowed {
		if !seen[rel] {
			t.Errorf("%s is allowlisted (%s) but no longer stats agentsync.toml — drop it from the allowlist", rel, reason)
		}
	}

	// NEGATIVE CONTROL — both arms, against a synthetic file set, so a guard
	// that has stopped biting fails HERE instead of passing silently.
	t.Run("negative control", func(t *testing.T) {
		violating := map[string]string{
			"internal/cli/sourceinit.go": "\tcfgInfo, err := os.Stat(filepath.Join(root, \"agentsync.toml\"))\n",
			"internal/cli/newprobe.go": "func ready(home string) bool {\n" +
				"\tfi, err := os.Stat(filepath.Join(home, \"agentsync.toml\"))\n" +
				"\treturn err == nil && fi.Mode().IsRegular()\n}\n",
			// Neither token alone is a probe: the guard must not flag these.
			"internal/cli/unrelated.go": "\tos.Stat(filepath.Join(home, \".state\"))\n\t_ = \"agentsync.toml\"\n",
		}
		unexpected, seen := scanProbeViolations(violating, allowed)
		if len(unexpected) != 1 || unexpected[0] != "internal/cli/newprobe.go" {
			t.Fatalf("the guard must flag a fourth stat-shaped probe and nothing else; got %v", unexpected)
		}
		if !seen["internal/cli/sourceinit.go"] {
			t.Fatal("the allowlisted probe contains the pattern in the synthetic set but was not seen")
		}

		// The stale-allowlist arm.
		_, seenStale := scanProbeViolations(map[string]string{"internal/cli/sourceinit.go": "no probe here"}, allowed)
		if seenStale["internal/cli/sourceinit.go"] {
			t.Fatal("an allowlisted file with no probe was reported as still holding one")
		}
	})
}
