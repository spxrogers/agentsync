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
// It is an INTERNAL test so it reads the real struct: a parallel view type in
// export_test.go would hide a newly-added field from its own guard.
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

// TestUpgradeNoticeNamesEveryRetiredCommand pins what the banner SAYS.
//
// The motivating bug shipped: after the top-level `update` was removed, the
// action line still read "`agentsync update` is deprecated". Every guard stayed
// green, because `upgrade_notice.go` is exempt from the stale-command scan (it
// names old spellings for a living) and that exemption is whole-file — it can
// catch a MISSING mention, never a WRONG one.
//
// Two later attempts to pin it were each defeated on the first try, so the
// shape below is chosen for what survived rather than for elegance:
//
//   - "no line says `deprecat`" fell to "still works; an alias is kept for one
//     more minor", and to a line naming a replacement that does not exist.
//   - "the line must also name the replacement" fell to "`agentsync update`
//     still works: it is now an alias for `agentsync plugin outdated` …", which
//     names both by construction, and to a REVERSED line — "`agentsync check`
//     … now spelled `agentsync verify`" — because an unordered co-occurrence
//     test reads a backwards sentence as a correct one.
//
// So the assertion is on the RELATIONSHIP between the two names, not on their
// presence and not on which words the line avoids:
//
//  1. Some action line names the retirement.
//  2. On that line the old spelling comes BEFORE its replacements — the
//     direction of the sentence is the claim it makes.
//  3. The connector between them — the text that carries the actual meaning —
//     must be one of a small ALLOWLIST of break phrases.
//
// Rule 3 is the load-bearing one, and it is an allowlist for the reason a
// blocklist kept failing: "legacy", "sunset", "no action needed yet", "is now
// an alias for" is an open set, while the ways agentsync is willing to say
// "this is gone" is a closed one we control. A new phrasing fails this test,
// and adding it here is a deliberate act a reviewer sees.
//
// It is still a structural approximation of a semantic contract. It cannot
// judge a sentence that is well-formed and false. What it does guarantee is
// that every retirement is named, points forward at a real replacement, and
// says so in wording someone signed off on.
func TestUpgradeNoticeNamesEveryRetiredCommand(t *testing.T) {
	if len(upgradeNotices) == 0 || len(retirements) == 0 {
		t.Fatal("no notices or no retirements; this guard would vacuously pass")
	}
	var lines []string
	for _, n := range upgradeNotices {
		lines = append(lines, n.Actions...)
	}

	for _, r := range retirements {
		var naming []string
		for _, line := range lines {
			if containsInvocation(line, "agentsync "+r.Old) {
				naming = append(naming, line)
			}
		}
		if len(naming) == 0 {
			t.Errorf("the upgrade notice never mentions `agentsync %s`, which was retired in favor of "+
				"`%s`. A user whose script calls it gets no explanation — the notice is the only channel "+
				"that reaches every install path, so an omission here is silent.", r.Old, r.replacementPhrase())
			continue
		}
		if !anyLineRetires(naming, r) {
			t.Errorf("no upgrade-notice line retires `agentsync %s` in favor of %s.\n"+
				"The line must name the old spelling FIRST, then every replacement, joined by one of the "+
				"sanctioned break phrases %v. A line that merely mentions both — or mentions them "+
				"backwards, or calls the old one an alias that still works — reads to a user as a grace "+
				"period they do not have.\n    lines naming it:\n      %s",
				r.Old, r.replacementPhrase(), sanctionedBreakPhrases,
				strings.Join(naming, "\n      "))
		}
	}

	// Headlines are scanned too: the headline is the most prominent line in the
	// banner, so "these commands are deprecated" there would be read first and
	// checked last. Cheap defense-in-depth on the one phrasing that shipped;
	// the rules above are what carry the weight.
	scanned := lines
	for _, n := range upgradeNotices {
		scanned = append(scanned, n.Headline)
	}
	for _, line := range scanned {
		if strings.Contains(strings.ToLower(line), "deprecat") {
			t.Errorf("an upgrade-notice line calls something deprecated:\n    %s\n"+
				"Nothing in this release is deprecated — the renames are hard and alias-free. Saying "+
				"otherwise promises a grace period that does not exist, to the exact audience whose "+
				"automation already broke.", line)
		}
	}
}

// sanctionedBreakPhrases is the closed set of ways the banner may say "this
// spelling is gone". The connector between the old name and its replacement
// must be exactly one of these, after normalization.
//
// Keep it short. Every entry is a promise about tone as well as meaning, and
// the test exists precisely so that inventing a new one is a decision rather
// than a slip.
var sanctionedBreakPhrases = []string{
	"is now",
	"moved to",
	"was removed (no alias) — use",
}

// anyLineRetires reports whether some line states the retirement: the old
// spelling, then a sanctioned break phrase, then every replacement.
func anyLineRetires(lines []string, r retirement) bool {
	for _, line := range lines {
		if lineRetires(line, r) {
			return true
		}
	}
	return false
}

// lineRetires checks one line. It finds where the old spelling ends and where
// the earliest replacement begins, requires that order, and requires the text
// between them to be a sanctioned break phrase.
func lineRetires(line string, r retirement) bool {
	oldAt := strings.Index(line, "agentsync "+r.Old)
	if oldAt < 0 {
		return false
	}
	oldEnd := oldAt + len("agentsync "+r.Old)

	first := -1
	for _, rep := range r.Replacements {
		// Every replacement must be named, and named AFTER the old spelling.
		at := indexAfter(line, "agentsync "+rep, oldEnd)
		if at < 0 || !containsInvocation(line[oldEnd:], "agentsync "+rep) {
			return false
		}
		if first < 0 || at < first {
			first = at
		}
	}
	if first < 0 {
		return false
	}
	connector := normalizeConnector(line[oldEnd:first])
	for _, ok := range sanctionedBreakPhrases {
		if connector == ok {
			return true
		}
	}
	return false
}

// indexAfter finds needle at or after off, returning an absolute index.
func indexAfter(s, needle string, off int) int {
	if off > len(s) {
		return -1
	}
	i := strings.Index(s[off:], needle)
	if i < 0 {
		return -1
	}
	return off + i
}

// normalizeConnector reduces the text between an old spelling and its
// replacement to comparable form: markdown backticks dropped, whitespace
// collapsed, case folded.
func normalizeConnector(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " "))
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
