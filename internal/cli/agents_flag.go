package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// `--agents` is the ONE grammar for "which agents does this RUN act on"
// (#200 F10). Before this there were three spellings of that idea:
// `status`/`diff` had `--agents`, `apply` had nothing at all, and `revert` took
// a positional agent plus `--all`. The daily loop is status → diff → reconcile →
// apply, so the filter you use in the first three silently not existing in the
// fourth was the sharpest edge.
//
// The five run-scoping commands — apply, status, diff, reconcile, revert —
// register it through addAgentsFlag and resolve it through selectAgents, so the
// split, the "*" convention, and the rejection messages cannot drift apart.
//
// DELIBERATE EXCEPTION — `mcp add --agents`. That flag is spelled the same and
// means something different, and it is NOT part of this grammar: it sets the
// PERSISTED `agents` field on the server being authored (which agents the
// server fans out to on every future apply), rather than narrowing the current
// run. It therefore does not go through addAgentsFlag/selectAgents — the
// canonical field it writes has its own validation, and "*" there is stored,
// not resolved. The same word for a persisted target set and a per-run filter
// is a wart we are keeping: `agents` is the canonical field's name
// (source.MCPServerSpec.Agents) and renaming the flag would desync the flag
// from the field it writes. Read `--agents` as "which agents" in both cases;
// the difference is whether the answer is remembered.

// addAgentsFlag registers the shared --agents filter. what names the operation
// for the help string ("report", "diff", "apply", …).
func addAgentsFlag(cmd *cobra.Command, target *string, what string) {
	cmd.Flags().StringVar(target, "agents", "",
		fmt.Sprintf(`limit the %s to a comma-separated agent allowlist ("*" = all enabled; default: all enabled)`, what))
}

// selectAgents narrows enabledAgents by the --agents flag. An unset flag means
// "all enabled"; "*" means the same explicitly; anything else is a validated
// allowlist. An empty value is rejected rather than silently meaning "none" —
// `--agents ""` in a script is a bug, and acting on nothing would hide it.
//
// enabled is the enabled-agent set the caller already built; enabledAgents is
// that set as a slice, returned unchanged when no filter is given.
func selectAgents(cmd *cobra.Command, enabledAgents []string, enabled map[string]bool, agentsCSV string) ([]string, error) {
	if !cmd.Flags().Changed("agents") {
		return enabledAgents, nil
	}
	names := splitAgents(agentsCSV)
	if len(names) == 0 {
		return nil, fmt.Errorf(`--agents cannot be empty; pass "*" for all enabled agents or name one or more`)
	}
	if containsStar(names) {
		return enabledAgents, nil
	}
	return resolveAgentFilter(names, enabled)
}
