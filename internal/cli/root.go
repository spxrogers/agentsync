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
	// Scope is declared ONCE and inherited (#200 F6). A command that cannot
	// honor it refuses rather than silently ignoring it — see scope_flags.go.
	cmd.PersistentFlags().String("scope", "", "user | project (default: user; prompts when run inside a project tree)")
	cmd.PersistentFlags().String("project", "", "explicit path to project root (implies --scope project)")

	cmd.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
		return enforceScopeStance(c)
	}

	// First-run-after-upgrade notice. This is the ONLY hook that reaches every
	// installation channel — `go install` has no post-install step, a Homebrew
	// cask's caveats print at install time only, and Scoop has nothing — so the
	// binary tells the user itself, once per machine, on stderr.
	//
	// Cobra runs only the CLOSEST PersistentPreRunE in the chain, so a
	// subcommand that grows its own would silently disable this. That is guarded
	// by TestNoSubcommandOverridesPersistentPreRun.
	cmd.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
		maybePrintUpgradeNotice(c)
		return nil
	}

	cmd.AddCommand(
		newInitCmd(),
		newAgentCmd(),
		newMigrateCmd(),
		newDoctorCmd(),
		newCheckCmd(),
		newApplyCmd(),
		newRevertCmd(),
		newStatusCmd(),
		newDiffCmd(),
		newReconcileCmd(),
		newMCPCmd(),
		newPluginCmd(),
		newMarketplaceCmd(),
		newSecretsCmd(),
		newExplainCmd(),
		newImportCmd(),
		newVersionCmd(),
	)
	// #200 F5: every peer component gets the READ side, so `mcp` is no longer the
	// only component you can ask about.
	cmd.AddCommand(newComponentListCmds()...)
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
