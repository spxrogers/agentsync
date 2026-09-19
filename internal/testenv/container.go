// Package testenv contains helpers used by tests to assert hermeticity
// preconditions. The headline helper is RequireContainer, which fast-fails
// any test that ought to run inside the agentsync hermetic container but is
// somehow being run on the host. Both guards also call ScrubAmbient, so a
// test's result can never depend on what the developer's shell happens to
// export (issue #270).
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

// ambientVars lists the non-AGENTSYNC_* environment variables production code
// reads that can move a test's result. Every AGENTSYNC_* escape hatch is
// scrubbed by prefix (see ScrubAmbient), so this list only has to name the
// third-party and universal-convention variables:
//
//   - GROK_HOME — Grok Build's own home override, read (through
//     paths.AgentHomeOverride) by the CLI's registry wiring; an ambient value
//     would move the grok adapter's user-scope root outside the test's HOME.
//   - NO_COLOR — https://no-color.org; ui.resolveColor honours it for ANY
//     value, so an exported one silently flips every colour-auto decision.
//   - EDITOR — `secret edit` launches it; tests that need one set it.
//
// When production code starts reading a new third-party variable, add it here
// (and route it through paths.Env), or the configured-environment CI leg
// cannot protect against it.
var ambientVars = []string{"GROK_HOME", "NO_COLOR", "EDITOR"}

// keepPrefixes are the AGENTSYNC_* families ScrubAmbient leaves alone: the
// harness's own signals (AGENTSYNC_TEST_IN_CONTAINER, AGENTSYNC_TEST_DEBUG, …)
// and the opt-in for the live network suite.
var keepPrefixes = []string{"AGENTSYNC_TEST_", "AGENTSYNC_LIVE_"}

// ScrubAmbient removes from the process environment every variable that could
// move a test's result but that no test asked for: every AGENTSYNC_* override
// (AGENTSYNC_HOME, AGENTSYNC_TARGET_ROOT, the AGENTSYNC_ALLOW_* hatches, …)
// except the harness's own AGENTSYNC_TEST_* / AGENTSYNC_LIVE_* signals, plus
// the variables in ambientVars. It returns the names it cleared, sorted by
// their position in the original environment (informational; callers may
// ignore it).
//
// It is process-wide and deliberately does NOT restore: an ambient value is
// never something a test wants back, and a test that needs a variable sets it
// itself with t.Setenv (whose cleanup restores "unset", consistent with the
// scrubbed baseline). RequireContainer and MustRunInContainer both call it, so
// every FS-touching package is covered by the guard it already has; a
// host-safe package with an env-sensitive decision (internal/ui's colour
// resolution) calls it from its own TestMain.
//
// Why this exists: `go test ./...` used to pass ONLY in an environment where
// none of our variables were set. CI is exactly that environment, so the
// suite had never been validated against a machine that actually uses
// agentsync — and an exported AGENTSYNC_HOME redded out 535 tests in
// internal/cli, the secret-leak guard among them (issue #270).
func ScrubAmbient() []string {
	var cleared []string
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !isAmbient(name) {
			continue
		}
		if err := os.Unsetenv(name); err != nil {
			// Unsetenv only fails on platforms that reject the name outright;
			// nothing to do but keep going.
			continue
		}
		cleared = append(cleared, name)
	}
	return cleared
}

// isAmbient reports whether ScrubAmbient clears name.
func isAmbient(name string) bool {
	if strings.HasPrefix(name, "AGENTSYNC_") {
		for _, keep := range keepPrefixes {
			if strings.HasPrefix(name, keep) {
				return false
			}
		}
		return true
	}
	for _, v := range ambientVars {
		if name == v {
			return true
		}
	}
	return false
}

// RequireContainer is the canonical guard for FS-touching tests
// (integration / e2e / bdd). It must be called as the first line of
// TestMain (or each TestXxx) in any package whose tests use t.TempDir(),
// AGENTSYNC_TARGET_ROOT, or otherwise write to disk.
//
// On host execution it calls t.Fatalf with a clear remediation message,
// which surfaces in `go test` output and short-circuits the rest of the
// test. Inside the container it scrubs the ambient environment
// (ScrubAmbient) and returns.
func RequireContainer(t testing.TB) {
	t.Helper()
	if InContainer() {
		ScrubAmbient()
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
// package runs; inside the container it scrubs the ambient environment
// (ScrubAmbient) once for the whole package. Use from TestMain like:
//
//	func TestMain(m *testing.M) {
//		testenv.MustRunInContainer()
//		os.Exit(m.Run())
//	}
func MustRunInContainer() {
	if InContainer() {
		ScrubAmbient()
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
