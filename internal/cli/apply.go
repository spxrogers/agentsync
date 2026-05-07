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

func newApplyCmd() *cobra.Command {
	var (
		dryRun bool
		scope  string
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "render canonical config and write per agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
			c, err := source.LoadWithCache(afero.NewOsFs(), home, pluginCacheRoot)
			if err != nil {
				return err
			}
			agents := []string{}
			for name, ag := range c.Config.Agents {
				if ag.Enabled {
					agents = append(agents, name)
				}
			}

			sc := adapter.ScopeUser
			if scope == "project" {
				sc = adapter.ScopeProject
			}

			reg := registryFactory()

			// Load state (needed for OwnedKeys injection in Plan).
			statePath := filepath.Join(home, ".state", "targets.json")
			s, err := state.Load(statePath)
			if err != nil {
				return err
			}

			if dryRun {
				plan, err := render.Plan(c, reg, agents, sc, "", s)
				if err != nil {
					return err
				}
				w := cmd.OutOrStdout()
				fmt.Fprintf(w, "Plan: %d ops total across %d agent(s)\n", plan.Total(), len(plan.PerAgent))
				for _, name := range reg.Names() {
					res, ok := plan.PerAgent[name]
					if !ok {
						continue
					}
					fmt.Fprintf(w, "  %-10s %d ops, %d skips\n", name, len(res.Ops), len(res.Skips))
				}
				report := render.BuildReport(c, plan, agents)
				if len(report.Rows) > 0 {
					fmt.Fprintln(w)
					report.PrintText(w)
				}
				return nil
			}

			// Real apply: render + write
			plan, err := render.Plan(c, reg, agents, sc, "", s)
			if err != nil {
				return err
			}
			if err := render.Apply(plan, reg); err != nil {
				return err
			}

			// Update state with post-apply hashes.
			for name, res := range plan.PerAgent {
				if err := render.RecordOpsState(s, name, sc, "", res.Ops); err != nil {
					return err
				}
			}
			if err := state.Save(statePath, s); err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			fmt.Fprintln(w, "applied:", plan.Total(), "ops")
			report := render.BuildReport(c, plan, agents)
			if len(report.Rows) > 0 {
				fmt.Fprintln(w)
				report.PrintText(w)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compute plan without writing destinations")
	cmd.Flags().StringVar(&scope, "scope", "user", "user | project")
	return cmd
}
