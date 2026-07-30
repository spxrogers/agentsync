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

	// cli.Execute owns the whole invocation and returns the exit code: the quiet
	// exit-code sentinel (status/diff --exit-code), the ✗ ERROR label, and the
	// --color resolution all live there, where the flag was parsed and where
	// ExitCoder is defined. main stays a three-line shim on purpose.
	os.Exit(cli.Execute())
}
