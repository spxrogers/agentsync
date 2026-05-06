// Package testenv contains helpers used by tests to assert hermeticity
// preconditions. The headline helper is RequireContainer, which fast-fails
// any test that ought to run inside the agentsync hermetic container but is
// somehow being run on the host.
package testenv

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// EnvVar is the explicit signal the agentsync test container sets at
// runtime. The hermetic entrypoint exports it; production environments
// never do.
const EnvVar = "AGENTSYNC_TEST_IN_CONTAINER"

// RequireContainer is the canonical guard for FS-touching tests
// (integration / e2e / bdd). It must be called as the first line of
// TestMain (or each TestXxx) in any package whose tests use t.TempDir(),
// AGENTSYNC_TARGET_ROOT, or otherwise write to disk.
//
// On host execution it calls t.Fatalf with a clear remediation message,
// which surfaces in `go test` output and short-circuits the rest of the
// test. Inside the container it is a fast no-op.
func RequireContainer(t testing.TB) {
	t.Helper()
	if InContainer() {
		return
	}
	t.Fatalf(`refusing to run on the host — this test touches the filesystem and
must run inside the agentsync hermetic container.

Use one of:
  just test          # unit + integration in container
  just test-e2e      # lifecycle e2e in container
  just test-bdd      # Gherkin BDD suite in container
  just test-release  # full release gate in container
  just test-fast     # pure-unit only on host (this test will not run)

Detection signals checked: env var %s, /.dockerenv, /run/.containerenv,
/proc/1/cgroup. None matched.`, EnvVar)
}

// MustRunInContainer is the TestMain-friendly counterpart to
// RequireContainer. It writes a clear message to stderr and exits
// non-zero when invoked outside a container, before any test in the
// package runs. Use from TestMain like:
//
//	func TestMain(m *testing.M) {
//		testenv.MustRunInContainer()
//		os.Exit(m.Run())
//	}
func MustRunInContainer() {
	if InContainer() {
		return
	}
	fmt.Fprintln(os.Stderr, "agentsync: refusing to run this test package on the host.")
	fmt.Fprintln(os.Stderr, "agentsync: tests in this package touch the filesystem and must run")
	fmt.Fprintln(os.Stderr, "agentsync: inside the hermetic container.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  just test          # unit + integration in container")
	fmt.Fprintln(os.Stderr, "  just test-e2e      # lifecycle e2e in container")
	fmt.Fprintln(os.Stderr, "  just test-bdd      # Gherkin BDD suite in container")
	fmt.Fprintln(os.Stderr, "  just test-release  # full release gate in container")
	fmt.Fprintln(os.Stderr, "  just test-fast     # pure-unit only on host")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "Detection signals checked: %s env var, /.dockerenv,\n", EnvVar)
	fmt.Fprintln(os.Stderr, "/run/.containerenv, /proc/1/cgroup. None matched.")
	os.Exit(1)
}

// InContainer reports whether this process is plausibly running inside a
// Linux container. It is intentionally permissive — any one of the
// following signals is enough:
//
//   - the agentsync entrypoint's AGENTSYNC_TEST_IN_CONTAINER=1 export
//   - /.dockerenv (docker convention)
//   - /run/.containerenv (podman convention)
//   - /proc/1/cgroup mentions docker / podman / containerd / kubepods
//
// We do not require ALL signals — engineers running ad-hoc shells inside
// the test image (e.g. `scripts/test-in-container.sh shell`) inherit the
// env var but might not have /.dockerenv mounted on every runtime. One
// signal is enough to honour the "running in a container" intent.
func InContainer() bool {
	if os.Getenv(EnvVar) == "1" {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if cgroup, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(cgroup)
		for _, marker := range []string{"docker", "podman", "containerd", "kubepods", "libpod"} {
			if strings.Contains(s, marker) {
				return true
			}
		}
	}
	return false
}
