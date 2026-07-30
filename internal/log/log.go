// Package log centralizes slog setup. The root cobra command installs the
// process-wide default logger in its PersistentPreRunE (internal/cli/root.go),
// which is what makes a slog.Warn from internal/render or internal/marketplace
// render as an agentsync diagnostic instead of a timestamped stdlib log line.
//
// That wiring IS this package. Without it, library-side slog falls through to
// slog's default handler — the shape #211 was reported against.
package log

import (
	"io"
	"log/slog"

	"github.com/spxrogers/agentsync/internal/ui"
)

// New returns a slog.Logger that renders through p's diagnostic vocabulary,
// writing to w. Debug level when verbose, otherwise Info.
//
// This level gates only slog-sourced diagnostics. It is not a global quiet
// switch: a command's own output and its direct p.Warnf calls are not slog
// records and are unaffected.
func New(w io.Writer, p *ui.Printer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(ui.NewSlogHandler(w, p, level))
}

// Install makes New's logger the process-wide slog default. Detach is the undo.
func Install(w io.Writer, p *ui.Printer, verbose bool) {
	slog.SetDefault(New(w, p, verbose))
}

// Detach points the process default at a discarding handler.
//
// For TEST BINARIES. A real process installs once and exits, so the handler's
// writer outlives it. A test binary runs many Execute() cycles, each leaving
// slog.Default() bound to that invocation's stderr — usually a *bytes.Buffer
// owned by a test that has already finished. A later library slog.Warn then
// writes into a dead buffer: silent today, a data race under t.Parallel.
//
// Discarding, not restoring the original: the original is the stdlib handler,
// whose timestamped stderr lines are the shape this package replaces. A test
// that wants to observe records installs its own handler.
func Detach() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}
