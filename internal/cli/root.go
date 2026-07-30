// Package cli wires cobra subcommands. NewRoot returns the root *cobra.Command
// with all subcommands attached.
package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

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
		// Install the process-wide slog default FIRST, before anything can log:
		// this is what makes a library warning (internal/render,
		// internal/marketplace) and a command's own p.Warnf identical (#211).
		//
		// printerOn, NOT newPrinter — an invalid --color must not SKIP the install.
		// Gating on `err == nil` left a bad value with no handler at all, so
		// library warnings reverted to the stdlib shape exactly when the user had
		// already made a mistake. printerOn degrades to `auto`; the value is still
		// validated by the command's own newPrinter call.
		//
		// Nothing here undoes the install, by design; aslog.Detach is the seam for
		// test binaries, which need it for the reason its doc gives. This package
		// calls it via t.Cleanup — see detachSlog in testhelper_test.go.
		installDiagnosticLogger(c, verbose)
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

// Execute builds and runs the command tree and returns the PROCESS EXIT CODE.
// main.go is the only caller: `os.Exit(cli.Execute())`.
//
// Owning the whole invocation — build, run, report, exit code — is what lets the
// terminal error be styled with no package-level state: `root` is still in scope
// after it returns, so colorModeOf reads the parsed `--color` flag directly rather
// than a package var carrying it across the main boundary.
//
// Two cobra details make that safe. One `*pflag.Flag` is shared between root and
// subcommands, so `colorModeOf(root)` sees a value parsed on a SUBCOMMAND's line.
// And when cobra fails before parsing flags (unknown subcommand, malformed flag)
// the value is unset and this yields ColorAuto — which is also why `mode, _`
// discards ParseColorMode's error: it returns ColorAuto alongside it.
func Execute() int { return executeRoot(NewRoot(), os.Stderr) }

// executeRoot is Execute with the root command and diagnostic stream injected, so
// a test can drive the real wiring rather than a copy of it.
func executeRoot(root *cobra.Command, errStream io.Writer) int {
	err := root.Execute()
	mode, _ := colorModeOf(root)
	return reportErrorTo(errStream, mode, err)
}

// reportErrorTo renders err as the CLI's terminal diagnostic on w and returns the
// process exit code. Writer and color mode are explicit so a test can exercise the
// formatting directly.
//
// This is the line #211 was reported against (see ui/diag.go): the last thing a
// user reads, printed unlabeled below a WARN. The `agentsync:` program prefix is
// gone with the fix — the ERROR label already says which stream this is.
func reportErrorTo(w io.Writer, mode ui.ColorMode, err error) int {
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
	// Bind BOTH streams to w: this diagnostic's destination is w, and ui resolves
	// `auto` per stream.
	//
	// sanitizeLines, not raw err.Error(): an error chain routinely carries
	// third-party agent-config text (a plugin id, a native subagent name, a
	// filesystem path), and no single call site can pre-sanitize a wrapped chain
	// assembled on the way up. Left raw, a bare \r in that text rewinds the
	// cursor over the "✗ ERROR" label and lets crafted config forge a "✅" line —
	// the terminal-spoofing class #93/#171 exist for. Sanitizing per line keeps a
	// legitimately multi-line error readable while stripping the control bytes.
	ui.New(w, w, mode).Errorf("%s", sanitizeLines(err.Error()))
	return 1
}

// sanitizeLines applies ui.Sanitize to each line and rejoins with "\n".
//
// ui.Sanitize STRIPS newlines (it does not escape them), so sanitizing a
// multi-line string wholesale would concatenate the lines with no separator and
// defeat the message-column indentation Fdiagf applies. Splitting first keeps
// the structure the author intended while still removing every control byte an
// attacker could smuggle in.
func sanitizeLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = ui.Sanitize(ln)
	}
	return strings.Join(lines, "\n")
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

// installDiagnosticLogger makes the slog default write to cmd's stderr, styled for
// that same stream.
//
// One writer, one expression, deliberately: passing the writer AND a separately
// built printer lets the two disagree, and an Out-bound printer means a library
// warning takes stdout's TTY-ness while writing to stderr. Deriving both from `ew`
// makes that unrepresentable — which matters because it also resists testing (two
// in-memory buffers cannot diverge under `auto`).
func installDiagnosticLogger(cmd *cobra.Command, verbose bool) {
	ew := cmd.ErrOrStderr()
	aslog.Install(ew, printerOn(cmd, ew), verbose)
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
// An unparseable --color degrades to auto rather than erroring: callers are
// mid-flow with no good way to surface a flag error, and the value is validated by
// newPrinter on the command's own path. (ParseColorMode returns ColorAuto with its
// error, so nothing branches on it.)
//
// Building a Printer per call is fine: Printer's "one per invocation" guidance is
// against re-deciding color inconsistently, and this cannot — same flag, same
// writer, same answer.
func printerOn(cmd *cobra.Command, w io.Writer) *ui.Printer {
	mode, _ := colorModeOf(cmd)
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
