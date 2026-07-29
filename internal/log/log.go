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
// function that restores the previous default. The CLI itself installs once per
// process and never restores; the restore exists for callers that must not leak
// a handler past the lifetime of the writer it holds.
func Install(w io.Writer, p *ui.Printer, verbose bool) func() {
	prev := slog.Default()
	slog.SetDefault(New(w, p, verbose))
	return func() { slog.SetDefault(prev) }
}

// Detach restores the process default to a handler that discards everything.
//
// This exists for TEST BINARIES. A real `agentsync` process installs once in the
// root command's PersistentPreRunE and exits, so the handler's writer outlives
// it. A test binary runs many Execute() cycles, and each one leaves
// slog.Default() bound to that invocation's stderr buffer — typically a
// *bytes.Buffer owned by a test that has already finished. A library slog.Warn
// reached later then writes into a dead buffer: silent today, and a data race
// the moment a test in that package adopts t.Parallel.
//
// Discarding rather than restoring the ORIGINAL default is deliberate: the
// stdlib default prints timestamped lines to stderr, so restoring it would spray
// exactly the output shape this package exists to replace across `go test`
// output. A test that wants to observe records installs its own handler.
func Detach() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}
