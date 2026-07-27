package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
)

// Test-only seams for the external cli_test package.
//
// This is a FUNCTION seam, not a data mirror. An earlier version of this file
// exported a hand-maintained copy of the upgradeNotice struct, which is the
// model-vs-artifact hazard CLAUDE.md names — a field added to the real type
// would have been invisible to its own guard. That guard now lives in an
// internal test and reads the real type. A setter has no such drift surface.

// StubSubagentMigrationPromptForTest forces the interactive subagent-migration
// offer to accept or decline, and restores the real prompter afterwards.
//
// The prompter is a package var precisely so tests can drive both branches
// without a terminal, but it is unexported — and the acceptance suite that
// needs it (plugin/apply/import lifecycles, with their marketplace fixtures)
// lives in cli_test. Without this seam those tests can only reach the decline
// path, which is how a self-deadlock on the ACCEPT path shipped unnoticed.
func StubSubagentMigrationPromptForTest(t *testing.T, accept bool) {
	t.Helper()
	prev := subagentMigrationPrompter
	subagentMigrationPrompter = func(*cobra.Command, *ui.Printer, *source.LegacySubagentDirError) bool {
		return accept
	}
	t.Cleanup(func() { subagentMigrationPrompter = prev })
}
