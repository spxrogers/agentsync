package cli

import (
	"fmt"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/source"
)

func newApplyCmd() *cobra.Command {
	var (
		dryRun bool
		scope  string
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "render canonical config and write per agent (M0: --dry-run only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun {
				return fmt.Errorf("M0 only supports --dry-run; real adapters arrive in M1 (claude) and M2 (opencode)")
			}
			home := paths.AgentsyncHome(paths.OSEnv{})
			c, err := source.Load(afero.NewOsFs(), home)
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
			plan, err := render.Plan(c, reg, agents, sc, "")
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
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compute plan without writing destinations")
	cmd.Flags().StringVar(&scope, "scope", "user", "user | project")
	return cmd
}
