package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/ui"
)

// TestImport_StyledAdapterWarnings is the integration regression for the
// warning-styling plumbing: a real adapter's Ingest warning — physically
// emitted into the adapter's configured Stderr from inside
// internal/adapter/<name>/ingest.go — must come out of the captured CLI
// buffer with the bold-yellow "⚠️ warning:" prefix the user sees on every
// other warning, NOT as plain "warning: …".
//
// Three load-bearing pieces:
//
//	internal/adapter/adapter.go   — WarnSink extension interface
//	internal/ui/ui.go             — WarnWriter + RouteTo helper
//	internal/cli/import.go        — wires them together once per run
//
// The routing primitive itself (RouteTo against a fake setter) is unit-tested
// in internal/ui/routeto_test.go. This integration test is what proves the
// wiring works end-to-end through a real adapter; it covers both the claude
// and opencode WarnSink implementations.
//
// **Which assertion is load-bearing?** The negative one. The CLI's own
// `importIO.warn` also writes through the same WarnWriter, so seeing a
// styled `⚠️ warning:` somewhere in `out` doesn't by itself prove the
// adapter line was routed — it could be a CLI-emitted warning being styled
// independently. The substring `warning: skill "…"` is emitted EXCLUSIVELY
// by the adapter's ingest path (claude/ingest.go's lenient-YAML notice for
// skills, opencode/ingest.go's equivalent). If routing breaks, that plain
// substring appears verbatim in the buffer; if routing works, the line
// starts with the styled prefix and the plain form is byte-for-byte absent.
//
// The styled-prefix positive check is kept as a smoke signal but is asserted
// loosely on the user-visible token (⚠️ + "warning:") + the presence of the
// two SGR codes (yellow + bold) — not on the exact concatenated ANSI byte
// sequence, which is an implementation detail of ui.Printer's wrap order
// that a correct refactor could shuffle.
func TestImport_StyledAdapterWarnings(t *testing.T) {
	for _, agent := range []string{"claude", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			tmp, env := importTestEnv(t)
			if _, err := runCLI(t, env, "agent", "add", agent); err != nil {
				t.Fatalf("agent add %s: %v", agent, err)
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
			// not a terminal). --color=auto would degrade to plain and the
			// styling assertions below would pass for the wrong reason.
			out, err := runCLI(t, env, "import", agent+":skill:rebaser", "--color=always")
			if err != nil {
				t.Fatalf("import: %v\n%s", err, out)
			}

			// LOAD-BEARING negative: the plain `warning: skill "rebaser"`
			// substring originates only in the adapter's Ingest path. If it
			// appears verbatim, routing was bypassed.
			plainPrefix := "warning: skill \"rebaser\""
			if strings.Contains(out, plainPrefix) {
				t.Fatalf("plain %q appeared — adapter stderr is not being routed through the styled writer:\n%q",
					plainPrefix, out)
			}

			// Smoke positive: the styled token shows up (user-visible glyph
			// + word), and the two SGR codes the WarnWriter applies are both
			// present. Asserted loosely so the test doesn't pin the exact
			// SGR concatenation order — `Yellow(Bold(…))` produces a
			// different byte sequence from `Bold(Yellow(…))` but is
			// visually identical and equally correct.
			styledToken := ui.GlyphWarnEmoji + " warning:"
			if !strings.Contains(out, styledToken) {
				t.Fatalf("expected the user-visible styled token %q in output; got:\n%q", styledToken, out)
			}
			const sgrYellow, sgrBold = "\x1b[33m", "\x1b[1m"
			if !strings.Contains(out, sgrYellow) || !strings.Contains(out, sgrBold) {
				t.Fatalf("expected both yellow (%q) and bold (%q) SGR codes in output; got:\n%q",
					sgrYellow, sgrBold, out)
			}
			// And the adapter's specific warning body must survive the
			// styling — otherwise the routing ate the line.
			if !strings.Contains(out, "rebaser") {
				t.Fatalf("styled prefix present but adapter warning body missing; routing may be malformed:\n%q", out)
			}
		})
	}
}

// skillsRoot returns the on-disk directory each adapter scans for a
// user-scope skill named "rebaser". Kept here rather than reaching into the
// adapter packages so a future adapter that joins this test only needs one
// case added. opencode intentionally shares ~/.claude/skills/ with claude —
// see opencode.ResolvePaths → ClaudeSkillsDir; both adapters surface the
// lenient-YAML warning from the same on-disk file.
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
