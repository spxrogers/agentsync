package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// `--agents` is the ONE grammar for "which agents does this act on" (#200 F10).
// Before this there were four: `status`/`diff` had `--agents`, `apply` had
// nothing at all, `revert` took a positional agent plus `--all`, and `mcp add`
// used `--agents` for a different thing (fan-out targeting, not filtering).
// The daily loop is status → diff → reconcile → apply, so the filter you use in
// the first three silently not existing in the fourth was the sharpest edge.
//
// Every command registers it through addAgentsFlag and resolves it through
// selectAgents, so the split, the "*" convention, and the rejection messages
// cannot drift apart.

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
