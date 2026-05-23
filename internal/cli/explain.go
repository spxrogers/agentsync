package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

func newExplainCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "explain <plugin-id>",
		Args:  cobra.ExactArgs(1),
		Short: "show per-agent translation for one plugin",
		RunE: func(cmd *cobra.Command, args []string) error {
			pluginID := args[0]
			home := paths.AgentsyncHome(paths.OSEnv{})
			pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
			c, err := source.LoadWithCache(afero.NewOsFs(), home, pluginCacheRoot)
			if err != nil {
				return err
			}

			// Find the plugin in the canonical model.
			var found bool
			for _, pl := range c.Plugins {
				label := pl.Plugin.ID
				if label == "" {
					label = pl.ID
				}
				if label == pluginID || pl.ID == pluginID {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("plugin %q not found; run 'agentsync plugin list' to see installed plugins", pluginID)
			}

			// Build a single-plugin canonical by filtering the Plugins slice.
			filtered := c
			var matchedPlugins []source.Plugin
			for _, pl := range c.Plugins {
				label := pl.Plugin.ID
				if label == "" {
					label = pl.ID
				}
				if label == pluginID || pl.ID == pluginID {
					matchedPlugins = append(matchedPlugins, pl)
				}
			}
			filtered.Plugins = matchedPlugins

			// Collect enabled agents.
			var agents []string
			for name, ag := range c.Config.Agents {
				if ag.Enabled {
					agents = append(agents, name)
				}
			}

			reg := registryFactory()
			statePath := filepath.Join(home, ".state", "targets.json")
			s, err := state.Load(statePath)
			if err != nil {
				return err
			}

			plan, err := render.Plan(filtered, reg, agents, adapter.ScopeUser, "", s, home)
			if err != nil {
				return err
			}

			report := render.BuildReport(filtered, plan, agents)

			w := cmd.OutOrStdout()
			if jsonOut {
				return report.PrintJSON(w)
			}
			report.PrintText(w)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "structured JSON output")
	return cmd
}
