// Package cli wires cobra subcommands. NewRoot returns the root *cobra.Command
// with all subcommands attached.
package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"

	aslog "github.com/spxrogers/agentsync/internal/log"
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

	// EXACTLY ONE PersistentPreRunE, composing every root-level pre-run concern.
	//
	// PersistentPreRunE is a plain field, so a second assignment in this
	// function silently DISCARDS the first — nothing fails to compile and no
	// test breaks, the dropped hook just quietly stops running. Wiring the
	// upgrade notice as its own assignment did exactly that to F6's scope
	// enforcement, and CI stayed green. Add new pre-run work INSIDE this
	// closure; TestRootDeclaresExactlyOnePersistentPreRun fails the build on a
	// second assignment, and cobra runs only the CLOSEST hook in the chain, so
	// TestNoSubcommandOverridesPersistentPreRun guards the same hazard one level
	// down.
	cmd.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
		// Install the process-wide slog default FIRST, before anything can log.
		// Library packages (internal/render, internal/marketplace) emit their
		// diagnostics through slog; with no handler installed those fell through
		// to the stdlib default and printed timestamped, unstyled lines that
		// looked nothing like the rest of the CLI (#211). Routing them here is
		// what makes a library warning and a command's own p.Warnf identical.
		//
		// This also records the resolved color decision for ReportError, which
		// runs in main() after cobra has returned and has no *cobra.Command to
		// read the flag off.
		if p, err := newPrinter(c); err == nil {
			resolvedColorMode = p.ColorMode()
			aslog.Install(c.ErrOrStderr(), p, verbose)
		}
		// The first-run-after-upgrade notice is the ONLY hook that reaches every
		// installation channel — `go install` has no post-install step, a
		// Homebrew cask's caveats print at install time only, and Scoop has
		// nothing — so the binary tells the user itself, once per machine, on
		// stderr. It is best-effort and returns nothing: a UX marker must never
		// fail a user's command.
		maybePrintUpgradeNotice(c)
		// Scope stance is the gate that can REFUSE the command, so it runs last
		// and owns the returned error (#200 F6).
		return enforceScopeStance(c)
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

// resolvedColorMode carries the --color decision from the root PersistentPreRunE
// out to ReportError, which runs in main() after cobra has returned and so has
// no *cobra.Command to read the flag from. It stays ColorAuto when the pre-run
// never got that far (an unparseable flag, an unknown subcommand), which is the
// right fallback: auto re-resolves against stderr's TTY-ness at print time.
//
// Package-scoped process state, which the rest of this package deliberately
// avoids. It is safe here because it is written exactly once per process, on
// the single-threaded path before any subcommand runs, and read exactly once
// after every subcommand has returned. A test that needs isolation should
// assert through ReportErrorTo, which takes its mode explicitly.
var resolvedColorMode = ui.ColorAuto

// ReportError prints err as the CLI's terminal diagnostic and returns the
// process exit code. main() is the only caller.
//
// The error a command returns is the LAST thing a user reads, and #211 showed
// what it cost to leave it unlabeled: a fatal `agentsync: render codex: …` sat
// flush against the left margin directly below a WARN line, with nothing —
// no glyph, no level, no color — marking it as the failure. Printing it through
// the same vocabulary as every other diagnostic is the fix. The `agentsync:`
// program prefix goes away with it: the ERROR label already says which stream
// this is, and the prefix only made the line longer.
func ReportError(err error) int { return ReportErrorTo(os.Stderr, resolvedColorMode, err) }

// ReportErrorTo is ReportError against an explicit writer and color mode, so a
// test can exercise the formatting without touching process state.
func ReportErrorTo(w io.Writer, mode ui.ColorMode, err error) int {
	if err == nil {
		return 0
	}
	// A quiet exit-code sentinel (status/diff --exit-code) carries its own
	// process exit code and an empty message: map it to that code and print
	// nothing, so a CI gate gets a stable non-zero exit with no spurious
	// diagnostic line.
	var ec ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	// Bind the printer's Out to w as well: resolveColor reads TTY-ness off Out,
	// and this diagnostic's stream is w.
	ui.New(w, w, mode).Errorf("%s", err)
	return 1
}

// newPrinter builds the presentation Printer for a command invocation, reading
// the inherited --color flag and binding to the command's stdout/stderr. The
// color decision (TTY + NO_COLOR + flag) is made once, here, so every command
// styles output identically. An invalid --color value is reported as an error.
func newPrinter(cmd *cobra.Command) (*ui.Printer, error) {
	mode, err := colorModeOf(cmd)
	if err != nil {
		return nil, err
	}
	return ui.New(cmd.OutOrStdout(), cmd.ErrOrStderr(), mode), nil
}

// colorModeOf reads the inherited --color flag off cmd.
func colorModeOf(cmd *cobra.Command) (ui.ColorMode, error) {
	modeStr, err := cmd.Flags().GetString("color")
	if err != nil {
		// Persistent flag not merged into this command's set; read it off the
		// inherited set explicitly.
		if f := cmd.InheritedFlags().Lookup("color"); f != nil {
			modeStr = f.Value.String()
		}
	}
	return ui.ParseColorMode(modeStr)
}

// printerOn builds a Printer whose BOTH streams are w, honoring --color. Use it
// for output that must land on one specific stream regardless of the command's
// stdout — the scope prompt, which goes to stderr so it can't corrupt a
// `--json` payload on stdout. Binding Out to w matters: ui resolves `auto`
// against Out's TTY-ness, so a printer bound to stdout would make the color
// decision for the wrong stream.
//
// An unparseable --color degrades to auto rather than erroring. The commands
// that call this are mid-flow and have no good way to surface a flag error; the
// value is validated (and reported) by newPrinter on the command's own path.
func printerOn(cmd *cobra.Command, w io.Writer) *ui.Printer {
	mode, err := colorModeOf(cmd)
	if err != nil {
		mode = ui.ColorAuto
	}
	return ui.New(w, w, mode)
}

// success writes a command's outcome line to its stdout through the shared
// emoji vocabulary. A convenience for the many small mutating commands
// (`agent add`, `mcp remove`, `secret set`, …) that have no *ui.Printer of
// their own; a command that already holds one should call p.Successf directly.
func success(cmd *cobra.Command, emoji, format string, args ...any) {
	printerOn(cmd, cmd.OutOrStdout()).Successf(emoji, format, args...)
}

// diag writes a labeled diagnostic to the command's stderr. Same convenience
// rationale as success — and same rule: diagnostics go to stderr, results go to
// stdout, regardless of which one the caller finds more convenient.
func diag(cmd *cobra.Command, level ui.Level, format string, args ...any) {
	w := cmd.ErrOrStderr()
	printerOn(cmd, w).Fdiagf(w, level, format, args...)
}

// emitJSON writes v as indented JSON to w. Used by the --json output modes,
// which print only the structured payload to stdout (diagnostics go to stderr)
// so the result is cleanly parseable.
func emitJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
