package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/cli"
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

// renderRaw executes a fresh root with args and returns the untrimmed bytes it
// wrote (stdout+stderr into one buffer). It deliberately does NOT go through
// runCLI, which strings.TrimSpaces the output and so would mask a trailing
// whitespace/newline divergence between the two version render paths.
func renderRaw(t *testing.T, args ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	root := cli.NewRoot()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute %v: %v", args, err)
	}
	// Copy out of the buffer so the returned slice can't alias reused storage.
	return append([]byte(nil), buf.Bytes()...)
}

// TestRoot_VersionCommandRawByteParity is the load-bearing regression test for
// the "can never drift" guarantee: `agentsync version` must be byte-for-byte
// identical to `agentsync --version`, including any trailing newline. It
// compares RAW, untrimmed bytes so that a future version-template edit which
// introduces a cobra template func (rendered by `--version` through cobra's
// FuncMap but not by a hand-rolled parse) cannot pass on one path while
// breaking the other.
func TestRoot_VersionCommandRawByteParity(t *testing.T) {
	cmdOut := renderRaw(t, "version")
	flagOut := renderRaw(t, "--version")
	if !bytes.Equal(cmdOut, flagOut) {
		t.Fatalf("`version` output %q != `--version` output %q", cmdOut, flagOut)
	}
}

// TestRoot_VersionCommandRejectsArgs asserts cobra.NoArgs rejects positional
// arguments to the alias subcommand.
func TestRoot_VersionCommandRejectsArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "one extra arg", args: []string{"version", "foo"}},
		{name: "two extra args", args: []string{"version", "foo", "bar"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			root := cli.NewRoot()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(tc.args)
			if err := root.Execute(); err == nil {
				t.Fatalf("expected non-nil error for %v, got nil (output %q)", tc.args, buf.String())
			}
		})
	}
}

// TestRoot_VersionCommandHelp asserts `agentsync version --help` exits 0 and
// prints the subcommand help (which contains its Short). Trimming is fine for a
// substring check, so this may use the runCLI helper.
func TestRoot_VersionCommandHelp(t *testing.T) {
	out, err := runCLI(t, nil, "version", "--help")
	if err != nil {
		t.Fatalf("version --help returned error: %v", err)
	}
	if !strings.Contains(out, "alias for --version") {
		t.Fatalf("version --help missing subcommand Short. Got: %s", out)
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
