package cli

import "strings"

// retirement describes one hard rename or removal shipped in v0.11.0 (#200
// F2/F4/F8). Every one is alias-free by explicit decision: the old spelling
// does not keep working.
//
// This table is the single oracle for THREE guards that would otherwise each
// carry their own copy of the list, and drift apart the first time a retirement
// is added to one and forgotten in the others:
//
//   - TestRetiredCommandsAreGone (command_surface_internal_test.go) — the old
//     spelling must not resurrect in the cobra tree, as a name or an alias.
//     Reads the TopLevel rows, since only those were command names.
//   - TestNoStaleRenamedCommandReferences (renamed_commands_guard_internal_test.go)
//     — no doc, comment, or hint may still tell a user to run the old spelling.
//     Reads the FailsAsUnknown rows: those are the ones that now error, so a
//     stale hint hands the user a broken command.
//   - TestUpgradeNoticeNamesEveryRetiredCommand (upgrade_notice_table_internal_test.go,
//     added by the stacked upgrade-notice branch) — the first-run banner must
//     name every retirement AND its replacement. Reads every row.
//
// The canonical `agents/` → `subagents/` directory move is deliberately absent:
// it is not a command retirement, and the banner's line for it is pinned
// separately by TestUpgradeNotice_ShownOnceOnUpgrade, which greps the rendered
// output for `migrate subagents`.
type retirement struct {
	// Old is the retired invocation WITHOUT the "agentsync " prefix, spelled
	// as a user would type it.
	Old string
	// New lists the replacement invocation(s), also without the prefix. All of
	// them must appear on the banner line that names Old — a retirement whose
	// notice says what broke but not what to run instead is half a notice.
	New []string
	// TopLevel marks a retirement that WAS a top-level cobra command name, so
	// the resurrection guard has something to look for. `plugin install` and
	// `explain <plugin>` are not: they were nested, and `explain` still exists.
	TopLevel bool
	// FailsAsUnknown marks a retirement whose old spelling now exits non-zero
	// as an unknown command. `explain <plugin>` is the one that does NOT — the
	// name still resolves and now answers a different question, which is why it
	// is the only rename in this release that can break a script silently.
	FailsAsUnknown bool
}

var retirements = []retirement{
	{Old: "verify", New: []string{"check"}, TopLevel: true, FailsAsUnknown: true},
	{Old: "secrets", New: []string{"secret"}, TopLevel: true, FailsAsUnknown: true},
	{
		Old:            "update",
		New:            []string{"plugin outdated", "plugin upgrade --all"},
		TopLevel:       true,
		FailsAsUnknown: true,
	},
	{Old: "plugin install", New: []string{"plugin add"}, FailsAsUnknown: true},
	{Old: "explain <plugin>", New: []string{"plugin explain"}},
}

// replacements renders r.New as a human phrase for a failure message.
func (r retirement) replacements() string { return strings.Join(r.New, " / ") }
