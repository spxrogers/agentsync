package cli

import (
	"sort"
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
			t.Errorf("no upgrade-notice line retires `agentsync %s` in favor of %s.\n\n"+
				"A retiring line must: name the old spelling FIRST, then every replacement; join them "+
				"with one of the sanctioned break phrases %q (replacements joined by %q); assert %q; and "+
				"claim no grace period (%q).\n\n"+
				"If you reworded a bullet and this failed, ADDING your phrasing to sanctionedBreakPhrases "+
				"is a legitimate fix — the allowlist exists to make that a decision, not to freeze the "+
				"wording.\n\n    lines naming it:\n      %s",
				r.Old, r.replacementPhrase(), sanctionedBreakPhrases, sanctionedJoiners,
				"no alias", continuityClaims, strings.Join(naming, "\n      "))
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
// than a slip. If a reworded bullet fails, ADDING the new phrasing here is a
// legitimate fix — the failure message says so.
var sanctionedBreakPhrases = []string{
	"is now",
	"moved to",
	"was removed (no alias) — use",
}

// sanctionedJoiners are the ways two replacements may be joined when a
// retirement has more than one. Constrained for the same reason as the
// connector: "`plugin outdated`, and definitely NOT `plugin upgrade --all`"
// otherwise reads as naming both.
var sanctionedJoiners = []string{"/", ",", "and", ", and"}

// continuityClaims are phrasings that promise the old spelling still works.
//
// This is the one blocklist here, and it does NOT compose with the required
// "(no alias" clause into completeness — an earlier version of this comment
// claimed it did, and a reviewer produced five survivors in one pass
// ("supported through 0.12", "keeps working for now", "is not yet removed",
// "remains available", "the legacy spelling is accepted"). Those are named
// below, and the next reviewer will find more: the set of ways to promise
// continuity in English is open, so no substring list closes it.
//
// What the rules around it DO close is the structural half — a line that omits
// the alias-free parenthetical, names its replacement backwards, joins two
// replacements with a negation, or connects them with unsanctioned wording all
// fail regardless of vocabulary. This list is a cheap net over the phrasings
// that have actually been attempted, not a proof.
var continuityClaims = []string{
	"deprecat", "still work", "an alias for", "alias is kept", "kept as an alias",
	"no action needed", "grace period", "will be removed", "for one minor",
	"one more minor", "until 0.", "supported through", "keeps working",
	"keep working", "not yet removed", "remains available", "legacy spelling",
	"both spellings",
}

// anyLineRetires reports whether some line states the retirement.
func anyLineRetires(lines []string, r retirement) bool {
	for _, line := range lines {
		if lineRetires(line, r) {
			return true
		}
	}
	return false
}

// lineRetires checks one line against every rule.
//
// The positional rules constrain the middle of the sentence; the whole-line
// rules constrain its edges. Both are needed: each design that had only one of
// them was broken by moving the false claim to the part it did not read.
func lineRetires(line string, r retirement) bool {
	lower := strings.ToLower(line)
	for _, bad := range continuityClaims {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	// Every retiring line asserts the absence of an alias, in the parenthesized
	// form all four shipped lines use: "(no alias)", "(no aliases)",
	// "(no alias; …)". Requiring the parenthetical rather than the bare words
	// is what stops the assertion being satisfied incidentally — "there is no
	// alias policy yet, so the old name is accepted" and "no aliases were
	// harmed; both spellings run" both contain "no alias" and both promise the
	// opposite of what this clause is supposed to mean.
	if !strings.Contains(lower, "(no alias") {
		return false
	}

	oldAt := indexInvocation(line, "agentsync "+r.Old, 0)
	if oldAt < 0 {
		return false
	}
	oldEnd := oldAt + len("agentsync "+r.Old)

	// Locate every replacement AFTER the old spelling, on a word boundary.
	//
	// Each span carries its OWN end. Sorting bare positions while indexing the
	// lengths in declaration order desynchronizes the two the moment a line
	// names the replacements in a different order than the table lists them —
	// which rejected a perfectly good reword and blamed the break phrase for it.
	spans := make([]span, 0, len(r.Replacements))
	for _, rep := range r.Replacements {
		i := indexInvocation(line, "agentsync "+rep, oldEnd)
		if i < 0 {
			return false
		}
		spans = append(spans, span{at: i, end: i + len("agentsync "+rep)})
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a].at < spans[b].at })

	if !isSanctioned(normalizeConnector(line[oldEnd:spans[0].at]), sanctionedBreakPhrases) {
		return false
	}
	// Consecutive replacements must be joined plainly, not by a clause that
	// negates or qualifies the one that follows.
	for k := 1; k < len(spans); k++ {
		if spans[k-1].end > spans[k].at {
			return false // overlapping matches; nothing sane to measure
		}
		if !isSanctioned(normalizeConnector(line[spans[k-1].end:spans[k].at]), sanctionedJoiners) {
			return false
		}
	}
	return true
}

// span is one replacement's location on a line: where it starts and where it
// ends. Both travel together through the sort.
type span struct{ at, end int }

func isSanctioned(s string, allowed []string) bool {
	for _, a := range allowed {
		if s == a {
			return true
		}
	}
	return false
}

// indexInvocation finds needle at or after off, on a trailing word boundary,
// returning an absolute index.
//
// Boundary-aware on purpose. A raw strings.Index made the guard CERTIFY a false
// claim: in "`agentsync verify` is now `agentsync checkpointless` — actually run
// `agentsync check`" it located the replacement inside `checkpointless`, read the
// connector as "is now", and passed. Prefix extension is live vocabulary in this
// table (`secret` inside `secrets`).
func indexInvocation(s, needle string, off int) int {
	if needle == "" || off > len(s) {
		return -1
	}
	for i := off; ; {
		j := strings.Index(s[i:], needle)
		if j < 0 {
			return -1
		}
		at := i + j
		end := at + len(needle)
		if end == len(s) || !isWordByte(s[end]) {
			return at
		}
		i = at + 1
	}
}

// normalizeConnector reduces the text between two invocations to comparable
// form: markdown backticks dropped, whitespace collapsed, case folded.
// Punctuation is preserved — the em dash is part of what makes a phrase a
// phrase, not noise.
func normalizeConnector(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " "))
}

// TestLineRetires exercises lineRetires directly.
//
// It needs its own table for the reason containsInvocation needed one: every
// real notice line passes, so an always-true lineRetires is indistinguishable
// from a working one by the guard that calls it. Three successive designs of
// this check were each broken by a reviewer within one attempt, and each
// evasion below is one of those attempts — kept as cases so the next rewrite
// cannot quietly reopen a hole an earlier one closed.
func TestLineRetires(t *testing.T) {
	upd := retirement{Old: "update", Replacements: []string{"plugin outdated", "plugin upgrade --all"}}
	ver := retirement{Old: "verify", Replacements: []string{"check"}}

	cases := []struct {
		name string
		line string
		r    retirement
		want bool
	}{
		// The shipped wording, in both sanctioned forms.
		{"removed-use form", "`agentsync update` was REMOVED (no alias) — use `agentsync plugin outdated` / `agentsync plugin upgrade --all`", upd, true},
		{"is-now form", "`agentsync verify` is now `agentsync check` (no alias) — doctor checks the MACHINE", ver, true},

		// Round 8: the bug that actually shipped.
		{"deprecated", "`agentsync update` is deprecated (no alias) — use `agentsync plugin outdated` / `agentsync plugin upgrade --all`", upd, false},

		// Round 9: named both, avoided the one banned word.
		{"alias-shaped", "`agentsync update` still works: it is now an alias for `agentsync plugin outdated` / `agentsync plugin upgrade --all`", upd, false},
		{"wrong replacement", "`agentsync update` is now `agentsync apply --force` (no alias)", upd, false},
		{"prose only", "note: agentsync updates the native config in place (no alias)", upd, false},

		// Round 10: unordered co-occurrence read a backwards sentence as correct.
		{"reversed", "`agentsync check` is now spelled `agentsync verify` (no alias)", ver, false},

		// Round 11: the same false promise, relocated outside the connector.
		{"tail continuity", "`agentsync update` was REMOVED (no alias) — use `agentsync plugin outdated` / `agentsync plugin upgrade --all` — the old spelling still works until 0.12.0", upd, false},
		{"lead continuity", "No action needed yet: `agentsync update` is now `agentsync plugin outdated` / `agentsync plugin upgrade --all` (no alias)", upd, false},
		{"alias kept in tail", "`agentsync update` was REMOVED (no alias) — use `agentsync plugin outdated` / `agentsync plugin upgrade --all`, but an alias is kept for one more minor", upd, false},
		{"second replacement negated", "`agentsync update` was REMOVED (no alias) — use `agentsync plugin outdated`, and definitely NOT `agentsync plugin upgrade --all`", upd, false},

		// Round 11: a raw index let a prefix extension certify a false claim.
		{"prefix extension", "`agentsync verify` is now `agentsync checkpointless` (no alias) — actually run `agentsync check`", ver, false},

		// The alias-free assertion is required, not incidental.
		{"missing no-alias clause", "`agentsync verify` is now `agentsync check`", ver, false},
		// An unsanctioned connector fails even when everything else is right.
		{"unsanctioned connector", "`agentsync verify` has been superseded by `agentsync check` (no alias)", ver, false},
		// Only one replacement named.
		{"partial replacements", "`agentsync update` was REMOVED (no alias) — use `agentsync plugin outdated`", upd, false},
		// Replacements named in a different order than the table declares them.
		// A legitimate reword: it must PASS. Sorting positions while indexing
		// lengths in declaration order rejected it, and blamed the break phrase.
		{"replacements reordered", "`agentsync update` was REMOVED (no alias) — use `agentsync plugin upgrade --all` / `agentsync plugin outdated`", upd, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lineRetires(tc.line, tc.r); got != tc.want {
				t.Errorf("lineRetires() = %v, want %v for:\n    %s", got, tc.want, tc.line)
			}
		})
	}
}

// TestRetiringSpellings gives the banner-prose parser the table every other
// helper here earned, and for the same reason: it is the only input to
// TestEveryRetiringNoticeLineHasATableRow, whose passing state is "found
// nothing unclaimed" — so a parser that extracted nothing at all would look
// identical to a correct one, below even the claimed == 0 floor.
func TestRetiringSpellings(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
	}{
		// The shipped forms, including the line that retires TWO spellings.
		{"is-now form", "`agentsync verify` is now `agentsync check` (no alias) — doctor checks the MACHINE", []string{"verify"}},
		{"two on one line", "`agentsync plugin install` is now `agentsync plugin add`, and `agentsync secrets` is now `agentsync secret` (no aliases)", []string{"plugin install", "secrets"}},
		{"removed-use form", "`agentsync update` was REMOVED (no alias) — use `agentsync plugin outdated` / `agentsync plugin upgrade --all`", []string{"update"}},
		{"moved-to form", "`agentsync explain <plugin>` moved to `agentsync plugin explain <plugin>` (no alias; the old name now explains a FILE)", []string{"explain <plugin>"}},

		// The trap: an ordinary sentence about a LIVE command. Reading this as
		// a retirement demanded a bogus table row for `status`, which would then
		// break the pinned set and the resurrection guard.
		{"live command, no replacement", "`agentsync status` is now scope-aware (no alias needed) — it reads the project tree", nil},
		{"live command, apply", "`agentsync apply` is now atomic per agent", nil},

		// A retiring clause with the old spelling unquoted must still parse:
		// requiring a backtick made a real retirement extract nothing, silently.
		{"unquoted old spelling", "agentsync foo is now `agentsync bar` (no alias)", []string{"foo"}},

		{"no invocation at all", "the canonical subagent directory moved: agents/ → subagents/", nil},
		{"unsanctioned phrasing", "`agentsync verify` has been superseded by `agentsync check`", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := retiringSpellings(tc.line)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("retiringSpellings() = %q, want %q\n    %s", got, tc.want, tc.line)
			}
		})
	}
}

// TestEveryRetiringNoticeLineHasATableRow is the reverse direction, and it is
// the half that was missing.
//
// Every other guard runs table→world: for each row, check the world agrees.
// Nothing ran world→table, so DELETING a row was completely silent — three
// reviewers each removed `secrets` from `retirements` and watched the whole
// package stay green, with that spelling then un-guarded in the resurrection
// check, the prose scan, and the banner at once. Deletion is the likelier
// mutation, too: a merge conflict resolved the wrong way, or someone
// "de-duplicating" `secret` and `secrets`.
//
// The banner is the external oracle. Each of its action lines that RETIRES
// something — an `agentsync <old>` followed by a sanctioned break phrase — must
// be claimed by a row. Orphan a clause and this fails.
func TestEveryRetiringNoticeLineHasATableRow(t *testing.T) {
	claimed := 0
	for _, n := range upgradeNotices {
		for _, line := range n.Actions {
			for _, spelling := range retiringSpellings(line) {
				found := false
				for _, r := range retirements {
					if r.Old == spelling {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("the upgrade notice retires `agentsync %s`, but no row in `retirements` "+
						"claims it:\n    %s\n"+
						"That spelling is therefore un-guarded everywhere — the resurrection check, the "+
						"stale-prose scan, and this file's own content guard all read that table.",
						spelling, line)
					continue
				}
				claimed++
			}
		}
	}
	if claimed == 0 {
		t.Fatal("no notice line was recognised as retiring anything; this guard would vacuously pass")
	}
}

// retiringSpellings extracts the old spellings a notice line retires: an
// `agentsync <words>` followed by a sanctioned break phrase AND then by another
// `agentsync` invocation.
//
// That last requirement is the whole difference between a guard and a trap. An
// earlier version accepted spelling + phrase alone, so an ordinary future line
// like "`agentsync status` is now scope-aware (no alias needed)" was read as
// retiring `status` — and the failure told the author that spelling was
// "un-guarded everywhere", steering them to add a bogus row that would then
// break the pinned set AND the resurrection guard, since `status` still
// resolves. A retirement says what replaces it; a sentence that names no
// replacement is not one.
//
// The spelling is found by trying successively longer word prefixes rather than
// by hunting a closing backtick, so a bare (unquoted) old spelling parses the
// same way lineRetires reads it. Requiring a backtick made a real retiring
// clause extract nothing at all, which is silent — the failure mode this guard
// exists to prevent.
func retiringSpellings(line string) []string {
	const prefix = "agentsync "
	var out []string
	for i := 0; ; {
		j := strings.Index(line[i:], prefix)
		if j < 0 {
			return out
		}
		at := i + j
		rest := line[at+len(prefix):]
		if sp, ok := retiringSpellingAt(rest); ok {
			out = append(out, sp)
		}
		i = at + len(prefix)
	}
}

// retiringSpellingAt reads one candidate spelling out of the text following an
// "agentsync " prefix, returning it only if a sanctioned break phrase and then
// another invocation follow.
func retiringSpellingAt(rest string) (string, bool) {
	// maxSpellingWords bounds the search: the longest retired spelling is two
	// words plus a placeholder ("explain <plugin>", "plugin install").
	const maxSpellingWords = 3
	words := strings.Fields(rest)
	if len(words) > maxSpellingWords {
		words = words[:maxSpellingWords]
	}
	for n := 1; n <= len(words); n++ {
		spelling := strings.TrimRight(strings.Join(words[:n], " "), "`,.;:")
		idx := strings.Index(rest, spelling)
		if idx < 0 {
			continue
		}
		after := strings.TrimLeft(rest[idx+len(spelling):], "` ")
		norm := normalizeConnector(after)
		for _, phrase := range sanctionedBreakPhrases {
			if norm != phrase && !strings.HasPrefix(norm, phrase+" ") {
				continue
			}
			// A retirement names what replaces it. Without this, any "is now"
			// sentence about a live command reads as a retirement.
			if strings.HasPrefix(strings.TrimPrefix(norm, phrase), " agentsync ") {
				return spelling, true
			}
		}
	}
	return "", false
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
