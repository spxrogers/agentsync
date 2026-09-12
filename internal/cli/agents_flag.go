package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/spxrogers/agentsync/internal/source"
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

// enabledAgentNames answers "which agents is this run allowed to touch" in the
// exact pair selectAgents takes: the enabled names as a slice, and the same set
// as a membership map for the --agents allowlist check.
//
// Eight commands needed that pair and each open-coded the same five-line map
// walk (apply, status, diff, reconcile, explain, plugin upgrade --lossless,
// plugin explain, plugin poll). Four of them also built the `enabled` map,
// four did not, and two re-sorted afterwards — so "enabled agents" had two
// shapes and three orderings depending on which file you read. One helper
// means a change to what "enabled" means (a future per-scope override, say)
// lands once.
//
// The result is SORTED. The map walk it replaces was unordered, so every caller
// already had to be order-insensitive; sorting only removes a source of
// nondeterminism (render.Plan visits agents in this order, so an error from two
// mis-rendering agents used to name whichever one the runtime happened to reach
// first). explain and plugin explain sorted explicitly for exactly this reason
// — that sort now lives here, once, for all of them.
//
// Both returns are non-nil even when nothing is enabled: callers test len(), and
// an empty map is a valid "nothing is enabled" answer for selectAgents.
func enabledAgentNames(cfg source.Config) ([]string, map[string]bool) {
	names := make([]string, 0, len(cfg.Agents))
	enabled := make(map[string]bool, len(cfg.Agents))
	for name, ag := range cfg.Agents {
		if ag.Enabled {
			names = append(names, name)
			enabled[name] = true
		}
	}
	sort.Strings(names)
	return names, enabled
}
