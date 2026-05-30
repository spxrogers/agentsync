package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/ui"
)

// TestImport_StyledAdapterWarnings is the integration regression for the
// warning-styling plumbing: a real adapter's Ingest warning — physically
// emitted into the adapter's configured Stderr from inside
// internal/adapter/<name>/ingest.go — must come out of the captured CLI
// buffer with the bold-yellow "⚠️ warning:" prefix the user sees on every
// other warning, NOT as plain "warning: …", and NOT only as a styled
// "warning:" that the CLI itself could have emitted.
//
// Three load-bearing pieces:
//
//	internal/adapter/adapter.go   — WarnEmitter extension interface
//	internal/ui/ui.go             — WarnWriter + RouteTo helper
//	internal/cli/import.go        — wires them together once per run
//
// The routing primitive itself (RouteTo against a fake setter, restore
// closure, typed-nil guard, non-implementor) is unit-tested in
// internal/ui/routeto_test.go. This integration test is what proves the
// wiring works end-to-end through a real adapter; the per-adapter
// behavioural nil-reset contract is pinned by TestSetStderr_NilResetsToDefault
// in each adapter package.
//
// **Which assertion is load-bearing?** The same-line regex below
// (styled "⚠️ warning:" prefix appearing on the same line as a
// lenient-YAML-specific token). Reasoning:
//
//   - A plain `"warning: …"` substring is not a sufficient negative: if
//     the adapter's stderr falls back to the test-process's real
//     os.Stderr (the failure mode the routing is meant to prevent), the
//     warning would be invisible to the captured buffer, and a
//     looser-positive ("the styled prefix appears anywhere in out")
//     would pass for the wrong reason — the CLI's own importIO.warn
//     writes through the SAME WarnWriter and emits its own styled
//     prefixes for unrelated warnings.
//   - The lenient-YAML notice ("frontmatter is not strict YAML; parsed
//     leniently") is emitted ONLY by the adapter's Ingest path. Requiring
//     it on the SAME line as the styled prefix proves the adapter line
//     was both received AND styled — neither a CLI-only styled warning
//     nor a routing-bypassed adapter warning matches the regex.
func TestImport_StyledAdapterWarnings(t *testing.T) {
	// Built once: the same-line regex requires the bold-yellow "⚠️ warning:"
	// prefix (verbatim — the user-visible bytes) followed by anything up to
	// the lenient-YAML phrase the adapter emits. ANSI byte order is not
	// pinned, just the presence and the same-line locality.
	//
	// `[^\n\r]*` rather than `[^\n]*`: rejects both newline AND carriage-
	// return between the prefix and the phrase. SGR bytes don't contain
	// either, so this only matters as defence-in-depth against a future
	// styling change that emits stray CRs — without the \r in the
	// negated class, a `\r` between the prefix and the phrase would let
	// the regex match across what the terminal would render as separate
	// lines.
	styledAdapterWarn := regexp.MustCompile(
		regexp.QuoteMeta(ui.GlyphWarnEmoji+" warning:") +
			`[^\n\r]*frontmatter is not strict YAML`,
	)

	for _, agent := range []string{"claude", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			tmp, env := importTestEnv(t)
			if _, err := runCLI(t, env, "agent", "add", agent); err != nil {
				t.Fatalf("[%s] agent add: %v", agent, err)
			}

			// Both adapters emit a lenient-YAML warning when a skill's
			// description carries an unquoted colon-space. We plant the
			// fixture under each agent's native skills root.
			skillDir := skillsRoot(t, tmp, agent)
			if err := os.MkdirAll(skillDir, 0o755); err != nil {
				t.Fatal(err)
			}
			body := "---\nname: rebaser\ndescription: Triggers on: rebase\n---\nRebase helper.\n"
			if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			// --color=always forces ANSI on a non-TTY (the runCLI buffer is
			// not a terminal). Without it, --color=auto would degrade to
			// plain and the styling assertion below would pass for the
			// wrong reason.
			out, err := runCLI(t, env, "import", agent+":skill:rebaser", "--color=always")
			if err != nil {
				t.Fatalf("[%s] import: %v\n%s", agent, err, out)
			}

			// LOAD-BEARING positive: the styled prefix and the
			// adapter-specific lenient-YAML token must appear on the
			// SAME line. This rules out two ways the routing could be
			// silently broken:
			//   1. Adapter Stderr falls back to the test-process's real
			//      os.Stderr (so the warning is invisible to `out`) —
			//      the regex wouldn't match because the lenient-YAML
			//      phrase wouldn't be present at all.
			//   2. Adapter writes plain "warning: …" to the captured
			//      buffer somehow without going through the WarnWriter
			//      restyle — the regex wouldn't match because the
			//      styled prefix wouldn't lead the line.
			if !styledAdapterWarn.MatchString(out) {
				t.Fatalf("[%s] expected same-line styled prefix + adapter lenient-YAML phrase; got:\n%q",
					agent, out)
			}

			// LOAD-BEARING negative: the plain `warning: skill "…"`
			// substring originates only in the adapter's Ingest path. If
			// it appears verbatim in `out`, routing wasn't bypassed via
			// os.Stderr (the warning DID reach `out`) but was bypassed
			// via the WarnWriter restyle (no prefix swap). This catches
			// regressions in WarnWriter.emit or the line-buffer flush.
			plainPrefix := "warning: skill \"rebaser\""
			if strings.Contains(out, plainPrefix) {
				t.Fatalf("[%s] plain %q appeared — adapter stderr reached the buffer but the WarnWriter restyle didn't fire:\n%q",
					agent, plainPrefix, out)
			}
		})
	}
}

// skillsRoot returns the on-disk directory each adapter scans for a
// user-scope skill named "rebaser". Kept here rather than reaching into
// the adapter packages so a future adapter joining the table only needs
// one case added. opencode intentionally shares ~/.claude/skills/ with
// claude — see opencode.ResolvePaths → ClaudeSkillsDir; both adapters
// surface the lenient-YAML warning from the same on-disk file. (codex
// reads skills from ~/.agents/skills/, but the integration test gates
// codex behind v1Supported so it isn't included today.)
func skillsRoot(t *testing.T, tmp, agent string) string {
	t.Helper()
	switch agent {
	case "claude", "opencode":
		return filepath.Join(tmp, ".claude", "skills", "rebaser")
	default:
		t.Fatalf("no skills root mapping for agent %q", agent)
		return ""
	}
}
