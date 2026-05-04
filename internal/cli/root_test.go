package cli_test

import (
	"strings"
	"testing"
)

func TestRoot_VersionFlag(t *testing.T) {
	out, err := runCLI(t, nil, "--version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "agentsync") {
		t.Fatalf("version output missing 'agentsync': %s", out)
	}
}

func TestRoot_HelpListsSubcommands(t *testing.T) {
	out, _ := runCLI(t, nil, "--help")
	for _, sub := range []string{"init", "agent", "doctor", "verify", "apply"} {
		if !strings.Contains(out, sub) {
			t.Fatalf("--help missing subcommand %q. Got: %s", sub, out)
		}
	}
}
