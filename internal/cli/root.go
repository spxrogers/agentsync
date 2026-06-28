// Package cli wires cobra subcommands. NewRoot returns the root *cobra.Command
// with all subcommands attached.
package cli

import (
	"encoding/json"
	"io"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/ui"
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
	var (
		verbose   bool
		colorFlag string
	)

	cmd := &cobra.Command{
		Use:           "agentsync",
		Short:         "Centrally manage AI coding-agent configurations",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}
	cmd.SetVersionTemplate(`{{.Use}} {{.Version}} (commit ` + Commit + `, built ` + Date + `)
`)
	cmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose logging (in `status`, also expands collapsed skill directories)")
	cmd.PersistentFlags().StringVar(&colorFlag, "color", "auto", "colorize output: auto | always | never")
	cmd.PersistentFlags().Bool("no-input", false, "never prompt; fail instead when a choice is required (for headless/non-interactive use)")

	cmd.AddCommand(
		newInitCmd(),
		newAgentCmd(),
		newDoctorCmd(),
		newVerifyCmd(),
		newApplyCmd(),
		newRevertCmd(),
		newStatusCmd(),
		newDiffCmd(),
		newReconcileCmd(),
		newMCPCmd(),
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

// newPrinter builds the presentation Printer for a command invocation, reading
// the inherited --color flag and binding to the command's stdout/stderr. The
// color decision (TTY + NO_COLOR + flag) is made once, here, so every command
// styles output identically. An invalid --color value is reported as an error.
func newPrinter(cmd *cobra.Command) (*ui.Printer, error) {
	modeStr, err := cmd.Flags().GetString("color")
	if err != nil {
		// Persistent flag not merged into this command's set; read it off the
		// inherited set explicitly.
		if f := cmd.InheritedFlags().Lookup("color"); f != nil {
			modeStr = f.Value.String()
		}
	}
	mode, perr := ui.ParseColorMode(modeStr)
	if perr != nil {
		return nil, perr
	}
	return ui.New(cmd.OutOrStdout(), cmd.ErrOrStderr(), mode), nil
}

// emitJSON writes v as indented JSON to w. Used by the --json output modes,
// which print only the structured payload to stdout (diagnostics go to stderr)
// so the result is cleanly parseable.
func emitJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
