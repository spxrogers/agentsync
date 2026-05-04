package main

import (
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
		fmt.Fprintln(os.Stderr, "agentsync:", err)
		os.Exit(1)
	}
}
