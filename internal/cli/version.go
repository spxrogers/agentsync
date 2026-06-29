package cli

import (
	"text/template"

	"github.com/spf13/cobra"
)

// newVersionCmd registers `agentsync version` as an alias for the root
// `--version` flag. It renders the root's version template (set via
// SetVersionTemplate in NewRoot), so the two outputs can never drift.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information (alias for --version)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root := cmd.Root()
			t, err := template.New("version").Parse(root.VersionTemplate())
			if err != nil {
				return err
			}
			return t.Execute(cmd.OutOrStdout(), root)
		},
	}
}
