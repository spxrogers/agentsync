package cli

import "github.com/spf13/cobra"

func newInitCmd() *cobra.Command {
	return &cobra.Command{Use: "init", Short: "scaffold ~/.agentsync/", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
}
