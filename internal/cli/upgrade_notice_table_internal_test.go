package cli

import (
	"strings"
	"testing"
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
