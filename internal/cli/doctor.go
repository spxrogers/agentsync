package cli

import "github.com/spf13/cobra"

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{Use: "doctor", Short: "print environment + adapter detection", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
}
