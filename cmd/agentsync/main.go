package main

import (
	"os"

	"github.com/spxrogers/agentsync/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli.Version = version
	cli.Commit = commit
	cli.Date = date

	// ReportError owns the whole terminal-diagnostic decision — the quiet
	// exit-code sentinel (status/diff --exit-code), the ✗ ERROR label, the
	// color resolution — so the last line a failing run prints goes through
	// the same vocabulary as every other diagnostic in the CLI. It lives in
	// internal/cli rather than here because that is where the --color flag was
	// resolved and where ExitCoder is defined.
	if code := cli.ReportError(cli.Execute()); code != 0 {
		os.Exit(code)
	}
}
