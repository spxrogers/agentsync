// Package log centralizes slog setup. CLI commands receive *slog.Logger from
// the root cobra command's PersistentPreRun.
package log

import (
	"io"
	"log/slog"
)

// New returns a JSON slog.Logger writing to w. If verbose is true, level is
// Debug; otherwise Info.
func New(w io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}
