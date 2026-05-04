package cli

import "github.com/spf13/cobra"

func newApplyCmd() *cobra.Command {
	return &cobra.Command{Use: "apply", Short: "render canonical config and write per agent (M0: --dry-run only)", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
}
