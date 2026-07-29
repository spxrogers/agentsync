// Package log centralizes slog setup. The root cobra command installs the
// process-wide default logger in its PersistentPreRunE (see internal/cli/root.go),
// which is what makes a slog.Warn from deep inside internal/render or
// internal/marketplace render as an agentsync diagnostic rather than as a
// stdlib log line.
//
// That wiring is the point of this package. It previously returned a JSON
// handler that nothing ever installed, so every library-side slog call fell
// through to slog's default handler and printed via the standard log package —
// timestamped, uncolored, and shaped like nothing else the CLI emitted. #211
// showed what that cost: a `2026/07/28 15:03:45 WARN …` line sitting directly
// above an unlabeled fatal error, with nothing to tell the two apart.
package log

import (
	"io"
	"log/slog"

	"github.com/spxrogers/agentsync/internal/ui"
)

// New returns a slog.Logger that renders through p's diagnostic vocabulary,
// writing to w. If verbose is true the level is Debug; otherwise Info.
//
// Level here gates only slog-sourced diagnostics. It is not a global quiet
// switch: a command's own result output and its direct p.Warnf calls are not
// slog records and are unaffected.
func New(w io.Writer, p *ui.Printer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(ui.NewSlogHandler(w, p, level))
}

// Install makes New's logger the process-wide slog default and returns a
// function that restores the previous default. The restore exists for tests,
// which must not leak a handler bound to a finished test's buffer into the next
// test; the CLI itself installs once per process and never restores.
func Install(w io.Writer, p *ui.Printer, verbose bool) func() {
	prev := slog.Default()
	slog.SetDefault(New(w, p, verbose))
	return func() { slog.SetDefault(prev) }
}
