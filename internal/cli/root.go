// Package cli wires cobra subcommands. NewRoot returns the root *cobra.Command
// with all subcommands attached.
package cli

import (
	"github.com/spf13/cobra"
)

// version metadata; main.go injects via -ldflags. Tests use the literal
// strings below.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// NewRoot constructs the root command tree. Tests build their own root via
// this constructor so flag state is isolated per test.
func NewRoot() *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:           "agentsync",
		Short:         "Centrally manage AI coding-agent configurations",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}
	cmd.SetVersionTemplate(`{{.Use}} {{.Version}} (commit ` + Commit + `, built ` + Date + `)
`)
	cmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose logging")

	cmd.AddCommand(
		newInitCmd(),
		newAgentCmd(),
		newDoctorCmd(),
		newVerifyCmd(),
		newApplyCmd(),
		newStatusCmd(),
		newDiffCmd(),
		newReconcileCmd(),
		newPluginCmd(),
		newMarketplaceCmd(),
		newUpdateCmd(),
		newSecretsCmd(),
		newExplainCmd(),
		newImportCmd(),
	)
	return cmd
}

// Execute is the main.go entry point.
func Execute() error { return NewRoot().Execute() }
