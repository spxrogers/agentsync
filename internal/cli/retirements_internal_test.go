package cli

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// retirement describes one hard rename or removal shipped in v0.11.0 (#200
// F2/F4/F8). Every one is alias-free by explicit decision: the old spelling
// does not keep working.
//
// This table is the single oracle for three guards that would otherwise each
// carry their own copy of the list and drift apart the first time a retirement
// is added to one and forgotten in the others:
//
//   - TestRetiredCommandsAreGone (command_surface_internal_test.go) — the old
//     spelling must not resurrect in the cobra tree. Reads the top-level rows.
//   - TestNoStaleRenamedCommandReferences (renamed_commands_guard_internal_test.go)
//     — no doc or hint may still tell a user to run the old spelling. Reads the
//     FailsAsUnknown rows, since only those hand the user a broken command.
//   - TestUpgradeNoticeNamesEveryRetiredCommand (upgrade_notice_table_internal_test.go,
//     added by the stacked upgrade-notice branch) — the first-run banner must
//     name every retirement and its replacement. Reads every row.
//
// The canonical `agents/` → `subagents/` move is absent: it is not a command
// retirement, and the banner's line for it is pinned by
// TestUpgradeNotice_ShownOnceOnUpgrade, which greps for `migrate subagents`.
//
// TestRetirementsTableIsWellFormed keeps the table honest. Without it the table
// is self-certifying, and a one-row edit is silent rather than loud: dropping a
// flag removes that row from two guards with the whole suite still green, which
// was demonstrated, not assumed.
type retirement struct {
	// Old is the retired invocation WITHOUT the "agentsync " prefix, spelled
	// as a user would type it.
	Old string
	// Replacements lists what to run instead, also without the prefix. The
	// banner line naming Old must name all of them.
	Replacements []string
	// FailsAsUnknown records that the old spelling now exits non-zero as an
	// unknown command. `explain <plugin>` is the one that does NOT: the name
	// still resolves and answers a different question, which is why it is the
	// only rename in this release that can break a script silently. Asserted
	// against the live command tree rather than trusted.
	FailsAsUnknown bool
}

var retirements = []retirement{
	{Old: "verify", Replacements: []string{"check"}, FailsAsUnknown: true},
	{Old: "secrets", Replacements: []string{"secret"}, FailsAsUnknown: true},
	{
		Old:            "update",
		Replacements:   []string{"plugin outdated", "plugin upgrade --all"},
		FailsAsUnknown: true,
	},
	{Old: "plugin install", Replacements: []string{"plugin add"}, FailsAsUnknown: true},
	{Old: "explain <plugin>", Replacements: []string{"plugin explain"}},
}

// topLevel reports whether Old was a bare top-level command NAME, which is what
// the resurrection guard can look for in the cobra tree. It is derived, not
// declared: a retirement is top-level exactly when its old spelling is a single
// word, and a hand-set boolean here was proven to silently drop a row from two
// guards when cleared.
func (r retirement) topLevel() bool { return !strings.Contains(r.Old, " ") }

// replacementPhrase renders Replacements for a failure message.
func (r retirement) replacementPhrase() string { return strings.Join(r.Replacements, " / ") }

// splitInvocation separates a replacement into its command path and its flags.
//
// It stops collecting path words at the FIRST flag: everything after one is
// flag-land, so `check --scope project` is the command `check` with a flag,
// not a nonexistent `check project` subcommand. Placeholders (`<plugin>`,
// `[id]`) are dropped from the path — they stand for user input, not commands.
func splitInvocation(invocation string) (path, flags []string) {
	inFlags := false
	for _, w := range strings.Fields(invocation) {
		switch {
		case strings.HasPrefix(w, "-"):
			inFlags = true
			flags = append(flags, strings.TrimLeft(w, "-"))
		case inFlags:
			// A flag's value, not a subcommand.
		case strings.HasPrefix(w, "<"), strings.HasPrefix(w, "["):
			// A placeholder for user input.
		default:
			path = append(path, w)
		}
	}
	return path, flags
}

// TestRetirementsTableIsPinned is the completeness half.
//
// TestRetirementsTableIsWellFormed validates every row that is PRESENT. It says
// nothing about a row that is absent, and deletion is the easier mutation —
// a merge conflict resolved the wrong way, or someone "de-duplicating"
// `secret`/`secrets`. Three reviewers independently deleted this table's
// `secrets` row and watched the whole package stay green: that spelling then
// vanished from the resurrection guard, the prose scan, AND the upgrade banner
// at once, with nothing outside the table anchoring it.
//
// A pinned set is the same answer TestTopLevelCommandSurfaceIsPinned gives for
// commands, for the same reason: this is a shipped, immutable fact about
// v0.11.0, so changing it must be a deliberate edit a reviewer sees rather than
// a silent consequence of touching something nearby. Retiring a command in a
// LATER release means adding a row here, which is exactly the moment to also
// add its banner line.
func TestRetirementsTableIsPinned(t *testing.T) {
	want := []string{
		"explain <plugin>",
		"plugin install",
		"secrets",
		"update",
		"verify",
	}
	var got []string
	for _, r := range retirements {
		got = append(got, r.Old)
	}
	sort.Strings(got)
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("the retirements table changed.\n  have: %s\n  want: %s\n\n"+
			"This table is the oracle for three guards, so a row silently leaving it un-guards that "+
			"spelling everywhere at once. If the change is deliberate, update this list — and if you "+
			"ADDED a retirement, add its line to upgradeNotices too, or the banner will never mention it.",
			strings.Join(got, " | "), strings.Join(want, " | "))
	}
}

// TestRetirementsTableIsWellFormed is the guard on the guards' own oracle.
//
// Three tests read this table and nothing read the table itself, which made it
// self-certifying: every consumer had a vacuity check, but each fires only on
// TOTAL emptiness. The realistic mutation is one row, and it was silent —
// clearing a row's flags removed `secrets` from both the resurrection guard and
// the prose scan with the entire package still green. Worse, a Replacements
// entry naming a command that does not exist would satisfy all three guards
// while the banner sent users somewhere that errors.
//
// So: every row is checked for shape, its derived top-level bit for
// self-consistency, its FailsAsUnknown claim against the live cobra tree, and
// every replacement for actually resolving to a real command today.
func TestRetirementsTableIsWellFormed(t *testing.T) {
	if len(retirements) == 0 {
		t.Fatal("the retirements table is empty; three guards would vacuously pass")
	}
	root := NewRoot()
	seen := map[string]bool{}

	for _, r := range retirements {
		switch {
		case r.Old == "":
			t.Errorf("a retirement has an empty Old; it would match nothing in any guard")
			continue
		case seen[r.Old]:
			t.Errorf("duplicate retirement %q — the derived maps collapse it silently", r.Old)
		}
		seen[r.Old] = true

		// An empty Replacements leaves the notice guard with nothing to require,
		// so its "says what to run instead" half would have no content to check
		// for this row.
		if len(r.Replacements) == 0 {
			t.Errorf("retirement %q lists no Replacements — the notice guard's "+
				"names-the-replacement assertion silently passes for it", r.Old)
		}
		for _, rep := range r.Replacements {
			if rep == "" {
				t.Errorf("retirement %q has an empty replacement", r.Old)
				continue
			}
			path, flags := splitInvocation(rep)
			if len(path) == 0 {
				t.Errorf("retirement %q replacement %q names no command", r.Old, rep)
				continue
			}
			cmd, ok := resolveCommand(root, path)
			if !ok {
				t.Errorf("retirement %q points at `agentsync %s`, which does not resolve to a command.\n"+
					"The banner would send an already-broken user to a second broken command.", r.Old, rep)
				continue
			}
			// The flags matter as much as the command: `plugin upgrade --all`
			// is useless advice if --all stops existing, and stripping flags
			// before resolving would hide that.
			for _, f := range flags {
				if cmd.Flags().Lookup(f) == nil && cmd.InheritedFlags().Lookup(f) == nil {
					t.Errorf("retirement %q points at `agentsync %s`, but `%s` has no --%s flag.\n"+
						"The banner would hand an already-broken user a command that errors on its flags.",
						r.Old, rep, cmd.CommandPath(), f)
				}
			}
		}

		// FailsAsUnknown is a claim about the world, so check it against the
		// world: walk Old down the tree and see whether every word resolves.
		oldPath, _ := splitInvocation(r.Old)
		_, resolves := resolveCommand(root, oldPath)
		if r.FailsAsUnknown == resolves {
			t.Errorf("retirement %q declares FailsAsUnknown=%v, but `agentsync %s` %s in the command tree.\n"+
				"That flag decides whether the prose scan guards this spelling, so a wrong value "+
				"silently un-guards every doc that still names it.",
				r.Old, r.FailsAsUnknown, r.Old,
				map[bool]string{true: "DOES resolve", false: "does NOT resolve"}[resolves])
		}
	}
}

// resolveCommand resolves a command path in the tree. cobra's Find returns the
// deepest match plus the leftover args rather than an error for an unknown
// SUBcommand, so the leftovers are what decide it.
func resolveCommand(root *cobra.Command, path []string) (*cobra.Command, bool) {
	if len(path) == 0 {
		return nil, false
	}
	cmd, rest, err := root.Find(path)
	if err != nil || cmd == nil || len(rest) > 0 {
		return nil, false
	}
	return cmd, true
}
