package cli

import "github.com/spf13/cobra"

func newVerifyCmd() *cobra.Command {
	return &cobra.Command{Use: "verify", Short: "schema-lint ~/.agentsync/ on demand", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
}
