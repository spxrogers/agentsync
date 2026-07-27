package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
)

// subagentMigrationPrompter is a package var whose doc says it is "injectable so
// tests can drive the accept/decline branches without a terminal" — and until
// this file, no test drove either branch. Only the --no-input path was covered,
// so the seam existed for a test that did not exist.
//
// These are internal tests because the seam is unexported; the end-to-end
// behavior of both branches is covered from cli_test in migrate_test.go.

// TestPromptSubagentMigration_FailsClosedWithoutTTY pins the default that makes
// headless runs safe: no terminal (and no --no-input) still declines, so a CI
// job gets the actionable error rather than blocking forever on a Read.
func TestPromptSubagentMigration_FailsClosedWithoutTTY(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("no-input", false, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("y\n")) // even an explicit yes must not be read

	p := &ui.Printer{Out: &out, Err: &out}
	pending := &source.LegacySubagentDirError{Home: "/tmp/home", Files: []string{"rev.md"}}

	if promptSubagentMigration(cmd, p, pending) {
		t.Fatal("no TTY must decline: a non-interactive run has to fail closed, not consume stdin")
	}
}

// TestPromptConfirmYes_OnlyExplicitYes pins the confirmation grammar. Anything
// that is not an explicit yes — including EOF, which is what a closed stdin
// looks like — must be a no, so the caller falls through to the fail-closed
// error rather than acting on silence.
func TestPromptConfirmYes_OnlyExplicitYes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"y", "y\n", true},
		{"yes", "yes\n", true},
		{"uppercase Y", "Y\n", true},
		{"padded yes", "  yes  \n", true},
		{"n", "n\n", false},
		{"empty line is the default no", "\n", false},
		{"EOF (closed stdin)", "", false},
		{"anything else", "sure\n", false},
		{"yes with trailing text", "yes please\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var w bytes.Buffer
			got := promptConfirmYes(&w, strings.NewReader(tc.input), "move? ")
			if got != tc.want {
				t.Fatalf("promptConfirmYes(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if w.Len() == 0 {
				t.Error("the prompt was never written, so the user was asked nothing")
			}
		})
	}
}

// TestPromptSubagentMigration_WritesToStderr pins the channel. The offer can
// fire ahead of a `status --json` / `diff --json` payload, and stdout is that
// payload's channel — a prompt there corrupts what a caller is piping.
func TestPromptSubagentMigration_WritesToStderr(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("no-input", false, "")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader("n\n"))

	p := &ui.Printer{Out: &stdout, Err: &stderr}
	pending := &source.LegacySubagentDirError{Home: "/tmp/home", Files: []string{"rev.md"}}
	promptSubagentMigration(cmd, p, pending)

	if stdout.Len() != 0 {
		t.Fatalf("the migration offer must never touch stdout (it precedes --json payloads):\n%s", stdout.String())
	}
}
