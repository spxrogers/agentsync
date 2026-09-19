package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
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
//
// And it runs the package under a neutral, empty HOME (issue #270). Every test
// here is supposed to redirect with AGENTSYNC_TARGET_ROOT, but nothing enforced
// that, and one test ran a command with no redirect at all: it created
// $HOME/.agentsync/.state/agentsync.lock in the container user's — or, via the
// documented AGENTSYNC_TEST_IN_CONTAINER=1 host escape hatch, the developer's —
// real home. Pointing HOME at a throwaway directory turns that escape from a
// silent write into a visible one: after the run, anything left under the
// neutral HOME is listed and the package FAILS, so a test that forgets its
// redirect cannot pass. Tests that need a specific HOME set it with t.Setenv.
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
	home, err := os.MkdirTemp("", "agentsync-cli-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "testmain: create neutral HOME: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", home); err != nil {
		fmt.Fprintf(os.Stderr, "testmain: set neutral HOME: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	// Always report strays — a test that failed BECAUSE it hit the wrong home
	// needs this listing most — and turn a green run red on them.
	if strays := listTree(home); len(strays) != 0 {
		fmt.Fprintln(os.Stderr, "testmain: a test wrote into the neutral HOME instead of its AGENTSYNC_TARGET_ROOT redirect:")
		for _, s := range strays {
			fmt.Fprintf(os.Stderr, "  %s\n", s)
		}
		fmt.Fprintln(os.Stderr, "testmain: every cli test must set AGENTSYNC_TARGET_ROOT (or HOME) to a tmp dir (issue #270)")
		if code == 0 {
			code = 1
		}
	}
	// Leave the neutral dirs before removing them (best effort; os.Exit skips defers).
	_ = os.Chdir(os.TempDir())
	_ = os.RemoveAll(neutral)
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// listTree returns every path under root (relative), or nil for an empty tree.
func listTree(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, rel)
		return nil
	})
	return out
}
