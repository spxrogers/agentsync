package cli

import (
	"github.com/spf13/cobra"
)

// newVersionCmd registers `agentsync version` as an alias for the root
// `--version` flag. Rather than re-parsing the version template by hand, its
// RunE delegates to cobra's own version renderer: it re-dispatches the root
// command with its `--version` flag set, so the SAME precompiled,
// FuncMap-bearing template function (`trim`, `rpad`, `gt`, …) renders the SAME
// data against the SAME flag-owning (root) command as `agentsync --version`.
// The two outputs are therefore byte-identical by construction and can never
// drift — even if the version template later starts using a cobra template
// function. TestRoot_VersionCommandRawByteParity pins that guarantee.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information (alias for --version)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Re-dispatch the root with the `--version` flag form. Routing the
			// root's args to a flag (not a subcommand) makes cobra's Find()
			// return the root itself — which owns the version flag and a
			// non-empty Version — so cobra renders the version via its
			// precompiled *tmplFunc before reaching any RunE. Driving the flag
			// on the root and re-executing with the original args would instead
			// route back into this subcommand (whose own Version is empty) and
			// recurse.
			root := cmd.Root()
			root.SetArgs([]string{"--version"})
			return root.Execute()
		},
	}
}
