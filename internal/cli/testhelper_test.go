package cli_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/cli"
)

// runCLI runs the CLI with given args, returns stdout+stderr combined and
// the resulting error. Sets AGENTSYNC_TARGET_ROOT to the supplied tmp via env.
func runCLI(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	return runCLIWithStdin(t, env, "", args...)
}

// runCLIWithStdin is like runCLI but supplies stdinContent as stdin. Stdout and
// stderr are merged into one buffer (in write order) so substring assertions see
// everything the command printed; use runCLISplit / runCLIWithStdinSplit when a
// test must know WHICH stream a message landed on.
func runCLIWithStdin(t *testing.T, env map[string]string, stdinContent string, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root := cli.NewRoot()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	if stdinContent != "" {
		root.SetIn(strings.NewReader(stdinContent))
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	err := root.Execute()
	out, _ := io.ReadAll(&buf)
	return strings.TrimSpace(string(out)), err
}

// runCLISplit runs the CLI capturing stdout and stderr into SEPARATE buffers,
// returning (stdout, stderr, err) each trimmed of surrounding whitespace. Use it
// to assert stream separation — e.g. that a --json payload lands cleanly on
// stdout while prompts / warnings go to stderr.
func runCLISplit(t *testing.T, env map[string]string, args ...string) (string, string, error) {
	t.Helper()
	return runCLIWithStdinSplit(t, env, "", args...)
}

// runCLIWithStdinSplit is runCLISplit with stdin supplied.
func runCLIWithStdinSplit(t *testing.T, env map[string]string, stdinContent string, args ...string) (string, string, error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := cli.NewRoot()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	if stdinContent != "" {
		root.SetIn(strings.NewReader(stdinContent))
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	err := root.Execute()
	return strings.TrimSpace(outBuf.String()), strings.TrimSpace(errBuf.String()), err
}
