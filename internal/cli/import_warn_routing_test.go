package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImport_StyledAdapterWarnings is the regression for the warning-styling
// plumbing: an adapter's Ingest warnings — physically emitted by the claude
// adapter into its configured Stderr — must be routed through the same
// styled writer the CLI uses for its own warnings, picking up the bold-yellow
// "⚠️ warning:" prefix the user sees on every other warning.
//
// The contract has three load-bearing pieces:
//
//	internal/adapter/adapter.go     — WarnSink extension interface
//	internal/ui/ui.go               — WarnWriter + RouteTo helper
//	internal/cli/import.go          — wires them together once per run
//
// If any of those silently regresses (RouteTo no longer type-asserts, or
// import.go forgets to wrap stderr, or the adapter stops implementing
// SetStderr), the YAML-frontmatter notices the claude adapter emits would
// fall back to plain "warning: …" on os.Stderr — invisible to the test
// buffer, but jarring next to the styled "⚠️ warning:" lines in real use.
// This test forces --color=always (the runCLI buffer is not a TTY) and asserts
// the ANSI-prefixed form appears in the captured output.
func TestImport_StyledAdapterWarnings(t *testing.T) {
	tmp, env := importTestEnv(t)

	// Plant a skill with a lenient-YAML description ('Triggers on: rebase'
	// — the colon-space is what trips strict YAML and triggers the
	// lenient-parse warning the claude adapter emits during Ingest).
	skillDir := filepath.Join(tmp, ".claude", "skills", "rebaser")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: rebaser\ndescription: Triggers on: rebase\n---\nRebase helper.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// --color=always forces ANSI even off a TTY (the test buffer is not a
	// terminal). Without this, --color=auto would degrade to plain and the
	// assertion would pass for the wrong reason.
	out, err := runCLI(t, env, "import", "claude:skill:rebaser", "--color=always")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}

	// The adapter emits exactly: warning: skill "rebaser" frontmatter is not
	// strict YAML; parsed leniently (consider quoting values containing ': ')
	// The WarnWriter rewrites the line-start "warning: " into a bold-yellow
	// "⚠️ warning:" sequence. We assert (a) the rewritten prefix appears,
	// (b) the original "rebaser" content survives so we know it's the same
	// line, and (c) the plain "warning: skill" form does NOT appear (which
	// would mean the routing was bypassed).
	const styledPrefix = "\x1b[33m\x1b[1m⚠️ warning:\x1b[0m\x1b[0m"
	if !strings.Contains(out, styledPrefix) {
		t.Fatalf("expected adapter warnings to be styled as bold-yellow ⚠️ warning:; got:\n%q", out)
	}
	if !strings.Contains(out, "rebaser") {
		t.Fatalf("styled prefix present but the rebaser-skill warning content is missing; routing may have eaten it:\n%q", out)
	}
	if strings.Contains(out, "warning: skill \"rebaser\"") {
		t.Fatalf("plain 'warning: skill \"rebaser\"' appeared — adapter stderr is not being routed through the styled writer:\n%q", out)
	}
}
