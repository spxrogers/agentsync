package cli_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/cli"
	aslog "github.com/spxrogers/agentsync/internal/log"
)

// detachSlog unbinds the process-wide slog default from THIS invocation's
// buffers once the test using them is done.
//
// The root command installs a slog handler bound to its stderr, and a real
// process exits with it installed. A test binary does not: without this, every
// runCLI leaves slog.Default() writing into a *bytes.Buffer owned by a test that
// has already returned, so a later library slog.Warn writes into a dead buffer —
// invisible today, and a data race the moment any test here adopts t.Parallel.
// t.Cleanup rather than a defer so the detach happens after the CALLER's
// assertions, not when the helper returns.
func detachSlog(t *testing.T) {
	t.Helper()
	t.Cleanup(aslog.Detach)
}

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
	detachSlog(t)
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

// setenvAll applies env via t.Setenv on the calling (test) goroutine. The
// concurrency tests use it to pre-set the environment BEFORE spawning the
// goroutine that calls runCLI with a nil env: t.Setenv must never run off the
// test goroutine — when the test goroutine fails first (a t.Fatal on the
// contention timeout), a t.Setenv from the still-running spawned goroutine
// panics ("Setenv used after test ended").
func setenvAll(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
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
	detachSlog(t)
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
