package cli

import "github.com/spf13/cobra"

func newAgentCmd() *cobra.Command {
	return &cobra.Command{Use: "agent", Short: "manage which agents agentsync targets", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
}
