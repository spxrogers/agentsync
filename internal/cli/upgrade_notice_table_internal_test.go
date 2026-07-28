package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestUpgradeNoticeTableIsWellFormed guards the append-only rule the table
// states but nothing enforced. An ID is the durable key recorded in
// .state/last-run.json: renaming one re-shows its notice to every user who
// already dismissed it, and duplicating one makes the second unreachable.
//
// This is an INTERNAL test on purpose. The first version reached the unexported
// table through a hand-maintained `UpgradeNoticeView` copy in export_test.go —
// which is the model-vs-artifact hazard CLAUDE.md names outright: a sixth field
// added to upgradeNotice would be silently invisible to its own guard. Reading
// the real struct removes the parallel model and the seam with it.
func TestUpgradeNoticeTableIsWellFormed(t *testing.T) {
	if len(upgradeNotices) == 0 {
		t.Fatal("no notices defined; this guard would vacuously pass")
	}
	ids := map[string]bool{}
	for _, n := range upgradeNotices {
		switch {
		case n.ID == "":
			t.Errorf("notice with empty ID (%q): the ID is the recorded key", n.Headline)
		case ids[n.ID]:
			t.Errorf("duplicate notice ID %q — the second is silently unreachable", n.ID)
		}
		ids[n.ID] = true
		if n.Since == "" {
			t.Errorf("notice %q has no Since; the banner prints it", n.ID)
		}
		if n.Headline == "" {
			t.Errorf("notice %q has no Headline", n.ID)
		}
		if len(n.Actions) == 0 {
			t.Errorf("notice %q lists no actions, so it says what broke but not what to do", n.ID)
		}
		if !strings.HasPrefix(n.Path, "/") {
			t.Errorf("notice %q Path %q must start with '/' (it is appended to the docs base URL)", n.ID, n.Path)
		}
	}
}

// TestEveryNoticeHasAnUpgradingDocsSection pins the cross-reference the notice
// table asserts in prose: each entry links to the upgrading page, so a notice
// without a matching section ships a banner whose "read more" lands on nothing.
//
// The banner is the ONLY channel that reaches every install path (no packaging
// channel agentsync ships through has a usable post-install hook), so a dead
// link in it is the whole feature failing at the last step.
func TestEveryNoticeHasAnUpgradingDocsSection(t *testing.T) {
	root := repoRootFromCaller(t)
	page := readFileForGuard(t, root, "website/src/content/docs/reference/upgrading.mdx")
	for _, n := range upgradeNotices {
		heading := "## " + n.Since
		if !strings.Contains(page, heading+"\n") {
			t.Errorf("notice %q (since %s) has no %q section in upgrading.mdx — its "+
				"\"read more\" link lands on a page that does not document it", n.ID, n.Since, heading)
		}
	}
}

// TestUpgradeNoticeNamesEveryRetiredCommand pins the notice's actual CONTENT —
// the one thing this feature exists to get right, and the one thing nothing
// checked.
//
// The bug that motivated it shipped: after the top-level `update` command was
// removed outright, the action line still read "`agentsync update` is
// deprecated". Every guard stayed green. `upgrade_notice.go` is exempt from
// TestNoStaleRenamedCommandReferences — it names old spellings for a living —
// and that exemption is whole-file, so it can catch a MISSING mention but never
// a WRONG one. A human caught it, on a banner whose entire audience is people
// whose scripts just broke, and whose worst possible failure is telling them
// their command still works for a while.
//
// Two directions, both load-bearing:
//
//   - Every retired top-level spelling must be NAMED somewhere in the notice.
//     A retirement the banner omits is one a user discovers as an unexplained
//     "unknown command" — exactly what the banner exists to prevent. The list
//     is retiredCommands (command_surface_internal_test.go), shared so a
//     future retirement cannot be added to one and forgotten in the other.
//   - No action line may describe anything as deprecated. This release has no
//     deprecations and no aliases: everything listed is a hard break. "Is
//     deprecated" tells a reader their cron line has a grace period it does
//     not have, which is worse than saying nothing.
func TestUpgradeNoticeNamesEveryRetiredCommand(t *testing.T) {
	if len(upgradeNotices) == 0 || len(retiredCommands) == 0 {
		t.Fatal("no notices or no retired commands; this guard would vacuously pass")
	}
	var lines []string
	for _, n := range upgradeNotices {
		lines = append(lines, n.Actions...)
	}
	all := strings.Join(lines, "\n")

	for old, replacement := range retiredCommands {
		if !strings.Contains(all, "agentsync "+old) {
			t.Errorf("the upgrade notice never mentions `agentsync %s`, which was retired in favor of "+
				"`%s`. A user whose script calls it gets an unexplained \"unknown command\" — the notice "+
				"is the only channel that reaches every install path, so an omission here is silent.",
				old, replacement)
		}
	}
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), "deprecat") {
			t.Errorf("an upgrade-notice action line calls something deprecated:\n    %s\n"+
				"Nothing in this release is deprecated — the renames are hard and alias-free. Saying "+
				"otherwise promises a grace period that does not exist, to the exact audience whose "+
				"automation already broke.", line)
		}
	}
}

// TestNoCommandIsNamedCompletion guards the one soft spot in the notice's
// completion exemption.
//
// isCompletionRequest matches the literal name "completion" anywhere up the
// parent chain, because that is what cobra calls its generated
// completion-script command. If agentsync ever adds a real command by that name
// — or a subcommand under one — it would be silently exempted from the upgrade
// notice, and the failure would be invisible: the notice simply never fires for
// that path. Cobra's own generated command is excluded here by construction,
// since it is only added at Execute time and never appears in NewRoot's tree.
func TestNoCommandIsNamedCompletion(t *testing.T) {
	var offenders []string
	seen := 0
	walkCommands(NewRoot(), func(path string, c *cobra.Command) {
		seen++
		names := append([]string{c.Name()}, c.Aliases...)
		for _, n := range names {
			if n == "completion" {
				offenders = append(offenders, path)
			}
		}
	})
	if seen == 0 {
		t.Fatal("the command walk yielded nothing; this guard would vacuously pass")
	}
	if len(offenders) > 0 {
		t.Fatalf("these commands are named (or aliased) `completion`: %s\n"+
			"isCompletionRequest exempts that name from the first-run upgrade notice, so this command "+
			"would silently never show it. Rename the command, or narrow isCompletionRequest to cobra's "+
			"generated command only.", strings.Join(offenders, ", "))
	}
}
