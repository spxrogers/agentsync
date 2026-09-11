package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/source"
)

// loadProjectedForScope loads the canonical model with plugin projection AND
// the active project overlay applied, returning the merged canonical plus the
// resolved scope and project root. Every project-scope-aware command (apply,
// status, diff, reconcile, the `plugin upgrade` re-apply) goes through it so
// they project and overlay identically.
//
// At project scope the project's own source tree (<root>/.agentsync/) is loaded
// as a full canonical and overlaid onto the user canonical via project.Merge —
// a missing tree loads as empty, so the overlay is a no-op. lenient selects the
// read-only/diagnostic projection: a strict same-name plugin.json/entry conflict
// is resolved entry-wins with a warning rather than a hard error, so status/diff
// still show state. Mutating callers pass false so a conflict aborts before any
// write.
func loadProjectedForScope(cmd *cobra.Command, fs afero.Fs, home string, lenient bool) (source.Canonical, adapter.Scope, string, error) {
	scopeFlag, projectFlag := scopeFlagValues(cmd)
	return loadProjectedForScopeFlags(cmd, fs, home, scopeFlag, projectFlag, lenient)
}

// loadProjectedForScopeFlags is loadProjectedForScope over explicit flag
// values — see resolveScopeFlags for the one caller that needs it.
func loadProjectedForScopeFlags(cmd *cobra.Command, fs afero.Fs, home, scopeFlag, projectFlag string, lenient bool) (source.Canonical, adapter.Scope, string, error) {
	sc, projectRoot, err := resolveScopeFlags(cmd, scopeFlag, projectFlag, noInputFlag(cmd))
	if err != nil {
		return source.Canonical{}, sc, projectRoot, err
	}
	// Offer the retired-layout migration before loading. source.Load is already
	// fail-closed on an unmigrated agents/ tree; running the check here first
	// turns that refusal into a guided move for the commands users hit daily.
	if err := ensureSubagentLayout(cmd, home, sc, projectRoot); err != nil {
		return source.Canonical{}, sc, projectRoot, err
	}
	pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
	load := marketplace.LoadProjectedExcluding
	if lenient {
		load = marketplace.LoadProjectedLenient
	}
	// A project tree can suppress a (user- or project-scope) plugin's projected
	// components in this repo by carrying plugins/<id>.toml with `disabled = true`
	// — the dir-model successor to the M5 marker's `[plugins] disabled`. We read
	// those ids first and exclude them from BOTH projections so the plugin's MCP/
	// skills/hooks vanish at project scope (not just its record).
	var disabled []string
	projHome := ""
	if sc == adapter.ScopeProject && projectRoot != "" {
		projHome = project.Home(projectRoot)
		disabled, err = projectDisabledPlugins(fs, projHome)
		if err != nil {
			return source.Canonical{}, sc, projectRoot, fmt.Errorf("read project plugin disables: %w", err)
		}
	}
	c, err := load(fs, home, pluginCacheRoot, disabled)
	if err != nil {
		return source.Canonical{}, sc, projectRoot, err
	}
	if projHome != "" {
		pc, perr := load(fs, projHome, pluginCacheRoot, disabled)
		if perr != nil {
			return source.Canonical{}, sc, projectRoot, fmt.Errorf("load project source %s: %w", projHome, perr)
		}
		if err := requireProjectAgents(pc, projHome); err != nil {
			return source.Canonical{}, sc, projectRoot, err
		}
		c = project.Merge(c, pc)
	}
	return c, sc, projectRoot, nil
}

// requireProjectAgents rejects a project tree whose [agents] table is empty or
// absent. A project's agent declaration is authoritative for project scope
// (project.Merge never inherits the user's enabled agents), so an undeclared
// set can only mean "render to nothing" — which reads as a silent no-op, or
// worse, as per-machine behavior to a user expecting the old inheritance. Fail
// with the fix instead. A declared-but-all-disabled table is a deliberate
// state and passes; the enabled-agent extraction downstream reports it softly.
func requireProjectAgents(pc source.Canonical, projHome string) error {
	if len(pc.Config.Agents) > 0 {
		return nil
	}
	cfgPath := filepath.Join(projHome, "agentsync.toml")
	return fmt.Errorf("this project declares no agents: the [agents] table in %s is empty or missing, "+
		"and project scope renders only to agents the project itself declares (user-scope agents are not inherited); "+
		"run `agentsync agent add <name> --scope project` or add them to %s", cfgPath, cfgPath)
}

// projectDisabledPlugins returns the plugin ids a project tree marks disabled
// (plugins/<id>.toml with `disabled = true`). A missing tree loads as empty.
func projectDisabledPlugins(fs afero.Fs, projHome string) ([]string, error) {
	pc, err := source.Load(fs, projHome)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pc.Plugins {
		if p.Plugin.Disabled {
			out = append(out, p.ID.Unverified())
		}
	}
	return out, nil
}
