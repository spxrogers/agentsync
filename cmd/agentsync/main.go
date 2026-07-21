package main

import (
	"errors"
	"fmt"
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

	if err := cli.Execute(); err != nil {
		// A quiet exit-code sentinel (status/diff --exit-code) carries its own
		// process exit code and an empty message: map it to that code and print
		// nothing, so a CI gate gets a stable non-zero exit with no spurious
		// "agentsync:" line. Every other error exits 1 with its message.
		var ec cli.ExitCoder
		if errors.As(err, &ec) {
			os.Exit(ec.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "agentsync:", err)
		os.Exit(1)
	}
}
