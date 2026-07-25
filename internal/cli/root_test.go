package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/cli"
)

// TestRoot_VersionFlag is a smoke test: it only proves `--version` runs and
// prints the hardcoded command name. That assertion holds even if the version
// metadata were never injected, so it is NOT the wiring oracle — see
// TestRoot_VersionInjection for the end-to-end injection coverage.
func TestRoot_VersionFlag(t *testing.T) {
	out, err := runCLI(t, nil, "--version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "agentsync") {
		t.Fatalf("version output missing 'agentsync': %s", out)
	}
}

// TestRoot_VersionInjection is the in-process oracle for the cli-side half of
// the version-injection chain: cli.Version/cli.Commit/cli.Date → the cobra
// version template. Because those exported package vars are read by NewRoot()
// at construction time, assigning distinctive non-default sentinels here
// stands in for the values main() copies over at startup. The test asserts all
// three sentinels reach the rendered output of BOTH surfaces (`--version` and
// the `version` subcommand), so it goes red if a field is dropped from the
// template or the subcommand re-dispatch regresses — unlike
// TestRoot_VersionFlag, which only ever sees the literal command name.
//
// What it deliberately CANNOT see: the ldflags → cmd/agentsync/main.go leg
// (goreleaser's `-X main.version=…` hitting the actual package-main vars and
// the main→cli copy). Renaming `var version` in main.go would keep this test
// green while releases print "dev". That leg is pinned end-to-end by
// TestE2E_VersionLdflagsInjection (test/e2e/version_injection_test.go), which
// builds the real binary with the real flag spellings.
func TestRoot_VersionInjection(t *testing.T) {
	const (
		wantVersion = "9.9.9-test"
		wantCommit  = "deadbeef-test"
		wantDate    = "2026-01-02T03:04:05Z-test"
	)

	// cli.Version/Commit/Date are exported package vars shared across the whole
	// test binary; leaving them mutated would pollute sibling tests (e.g. the
	// raw-byte parity guard). Snapshot and restore around this test.
	origVersion, origCommit, origDate := cli.Version, cli.Commit, cli.Date
	t.Cleanup(func() {
		cli.Version, cli.Commit, cli.Date = origVersion, origCommit, origDate
	})
	// Assign sentinels BEFORE any runCLI call: runCLI builds the root via
	// cli.NewRoot(), which reads these vars (Version: and the version template)
	// at construction time.
	cli.Version, cli.Commit, cli.Date = wantVersion, wantCommit, wantDate

	sentinels := []struct {
		field string
		value string
	}{
		{field: "version", value: wantVersion},
		{field: "commit", value: wantCommit},
		{field: "date", value: wantDate},
	}

	cases := []struct {
		name string
		args []string
	}{
		{name: "flag", args: []string{"--version"}},
		{name: "subcommand", args: []string{"version"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runCLI(t, nil, tc.args...)
			if err != nil {
				t.Fatalf("runCLI %v: %v", tc.args, err)
			}
			for _, s := range sentinels {
				if !strings.Contains(out, s.value) {
					t.Fatalf("%v output missing injected %s %q; got: %s", tc.args, s.field, s.value, out)
				}
			}
		})
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
