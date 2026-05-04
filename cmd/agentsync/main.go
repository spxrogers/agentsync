package main

import (
	"fmt"
	"os"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("agentsync %s (commit %s, built %s)\n", version, commit, date)
		return
	}
	fmt.Println("agentsync — placeholder; cli wiring lands in Task 13")
}
