package cli

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestTopLevelCommandSurfaceIsPinned locks the v1.x top-level command set.
//
// This PR is the pre-1.0 lock-in of that surface: the renames are hard, with no
// aliases, so a command silently appearing or disappearing is a breaking change
// to every user's scripts. Nothing pinned the set.
//
// The BDD release smoke scenario (test/bdd/features/08_release_smoke.feature)
// looks like it covers this and does not: it asserts `--help` OUTPUT CONTAINS
// each name, and those names occur in other commands' one-line descriptions —
// "secret" appears in both `check`'s and `doctor`'s Short, so the assertion
// passes with the whole `secret` group deleted. Every `contains` in that
// scenario shares the weakness. Substring-matching help prose cannot pin a
// command set; comparing the set can.
//
// Adding or removing a top-level command is a deliberate act — update this list
// in the same commit, and the CHANGELOG with it.
func TestTopLevelCommandSurfaceIsPinned(t *testing.T) {
	want := []string{
		"agent", "apply", "check", "command", "diff", "doctor", "explain",
		"hook", "import", "init", "lsp", "marketplace", "mcp", "migrate",
		"plugin", "reconcile", "revert", "secret", "skill", "status",
		"subagent", "version",
	}

	var got []string
	for _, c := range NewRoot().Commands() {
		// cobra synthesizes `help` and `completion`; they are not our surface.
		switch c.Name() {
		case "help", "completion":
			continue
		}
		got = append(got, c.Name())
	}
	sort.Strings(got)

	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the top-level command surface changed.\n  have: %s\n  want: %s\n\n"+
			"This is the v1.x locked surface — adding or removing a command breaks or extends what "+
			"users script against. If the change is deliberate, update this list, the command tables in "+
			"docs/user-guide.md and website/src/content/docs/reference/cli.mdx, and CHANGELOG.md.",
			strings.Join(got, " "), strings.Join(want, " "))
	}
}

// TestRetiredCommandsAreGone is the negative half: the hard renames must not
// resurrect, as a command OR as an alias, at ANY depth.
//
// It complements TestRenamedGroupsHaveNoAliases (component_verbs_test.go),
// which invokes the old spellings and requires them to fail.
//
// What this adds is structural rather than behavioral: no fixture, no process,
// and it reads the alias list directly, so it still fires for a command that
// would exit non-zero for some unrelated reason (bad args, missing home) and
// thus look "gone" to an invocation test.
//
// It walks the WHOLE tree, not just the top level. An earlier version iterated
// `NewRoot().Commands()` and was proven blind: adding `Aliases: []string{"update"}`
// to `plugin upgrade` left the entire suite green, resurrecting `agentsync
// plugin update` — a spelling nobody decided to ship.
//
// That does mean the retired spellings are reserved tree-WIDE while the
// retirements themselves were top-level. A future `agentsync marketplace
// update` would fail here even though nothing was resurrected. That is the
// intended trade: these three words are the ones users' broken scripts still
// contain, so reusing any of them anywhere in this release's surface is a
// decision to make deliberately — by editing this table — not by accident.
func TestRetiredCommandsAreGone(t *testing.T) {
	gone := map[string]retirement{}
	for _, r := range retirements {
		if r.topLevel() {
			gone[r.Old] = r
		}
	}
	if len(gone) == 0 {
		t.Fatal("no top-level retirements in the table — this guard would vacuously pass")
	}

	checked := 0
	walkCommands(NewRoot(), func(path string, c *cobra.Command) {
		checked++
		for _, n := range append([]string{c.Name()}, c.Aliases...) {
			if r, isGone := gone[n]; isGone {
				t.Errorf("`%s` is reachable again as `agentsync %s` (a name or an alias) — it was retired "+
					"with no alias by explicit decision, replaced by `%s`, so resurrecting it silently "+
					"changes the contract", n, path, r.replacementPhrase())
			}
		}
	})
	if checked == 0 {
		t.Fatal("walked zero commands — this guard would vacuously pass")
	}
}
