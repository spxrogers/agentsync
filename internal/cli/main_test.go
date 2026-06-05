package cli_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestMain enforces the hermeticity contract: every test in this package
// touches the filesystem (tmp dirs, AGENTSYNC_TARGET_ROOT redirection,
// state files, etc.), so we refuse to run on the host. Use `just test`
// or `just test-release` to invoke through the hermetic container.
//
// It also runs the package from a neutral, empty working directory. agentsync
// now commits a real .agentsync/ project tree at the repo root; these tests run
// the CLI in-process, so they would otherwise inherit this package's directory
// as cwd, and the no-scope project auto-discovery in resolveScope would walk up,
// find the repo tree, and fail with the "no scope was given" ambiguity. From a
// neutral dir the default (no-scope) path resolves to user scope, exactly as it
// did before the repo was dogfooded. Tests that exercise project discovery
// chdir explicitly (see m5_integration_test.go) and are unaffected.
func TestMain(m *testing.M) {
	testenv.MustRunInContainer()
	neutral, err := os.MkdirTemp("", "agentsync-cli-cwd-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "testmain: create neutral cwd: %v\n", err)
		os.Exit(1)
	}
	if err := os.Chdir(neutral); err != nil {
		fmt.Fprintf(os.Stderr, "testmain: chdir to neutral cwd: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	// Leave the neutral dir before removing it (best effort; os.Exit skips defers).
	_ = os.Chdir(os.TempDir())
	_ = os.RemoveAll(neutral)
	os.Exit(code)
}
