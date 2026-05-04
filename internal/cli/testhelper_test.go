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
	var buf bytes.Buffer
	root := cli.NewRoot()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	for k, v := range env {
		t.Setenv(k, v)
	}
	err := root.Execute()
	out, _ := io.ReadAll(&buf)
	return strings.TrimSpace(string(out)), err
}
