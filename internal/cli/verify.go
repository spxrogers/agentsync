package cli

import (
	"fmt"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/source"
)

func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "schema-lint ~/.agentsync/ on demand",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			c, err := source.Load(afero.NewOsFs(), home)
			if err != nil {
				return fmt.Errorf("verify: %w", err)
			}
			for name := range c.Config.Agents {
				if err := validateAgent(name); err != nil {
					return fmt.Errorf("agents.%s: %w", name, err)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ok: schema valid; all referenced agents are recognized adapters")
			return nil
		},
	}
}
