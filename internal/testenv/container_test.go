package testenv_test

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestScrubAmbient pins which variables the harness neutralizes (issue #270):
// every AGENTSYNC_* override except the harness's own AGENTSYNC_TEST_* /
// AGENTSYNC_LIVE_* signals, plus the third-party / convention variables
// production code reads (GROK_HOME, NO_COLOR, EDITOR). Pure-unit: it only
// touches the process environment, via t.Setenv, which restores each variable
// when the test ends.
func TestScrubAmbient(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		cleared bool
	}{
		{name: "AGENTSYNC_HOME is cleared", key: "AGENTSYNC_HOME", cleared: true},
		{name: "AGENTSYNC_TARGET_ROOT is cleared", key: "AGENTSYNC_TARGET_ROOT", cleared: true},
		{name: "AGENTSYNC_ALLOW_* hatches are cleared", key: "AGENTSYNC_ALLOW_SYMLINK_DEST", cleared: true},
		{name: "a future AGENTSYNC_* knob is cleared by prefix", key: "AGENTSYNC_SOMETHING_NEW", cleared: true},
		{name: "GROK_HOME is cleared", key: "GROK_HOME", cleared: true},
		{name: "NO_COLOR is cleared", key: "NO_COLOR", cleared: true},
		{name: "EDITOR is cleared", key: "EDITOR", cleared: true},
		{name: "the container signal is kept", key: testenv.EnvVar, cleared: false},
		{name: "AGENTSYNC_TEST_DEBUG is kept", key: "AGENTSYNC_TEST_DEBUG", cleared: false},
		{name: "the live-suite opt-in is kept", key: "AGENTSYNC_LIVE_PLUGIN_TEST", cleared: false},
		{name: "HOME is kept", key: "HOME", cleared: false},
		{name: "an unrelated variable is kept", key: "UNRELATED_FOR_TESTENV", cleared: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// t.Setenv restores the ORIGINAL state (set or unset) on cleanup, so
			// a scrub inside the test never leaks past it — including the
			// AGENTSYNC_TEST_IN_CONTAINER value the rest of the run relies on.
			t.Setenv(tc.key, "sentinel")
			cleared := testenv.ScrubAmbient()
			_, present := os.LookupEnv(tc.key)
			if tc.cleared && present {
				t.Fatalf("%s survived ScrubAmbient; cleared=%v", tc.key, cleared)
			}
			if !tc.cleared && !present {
				t.Fatalf("%s was cleared by ScrubAmbient; it must be kept (cleared=%v)", tc.key, cleared)
			}
			if tc.cleared != slices.Contains(cleared, tc.key) {
				t.Fatalf("ScrubAmbient reported cleared=%v; want %s reported as cleared=%v", cleared, tc.key, tc.cleared)
			}
		})
	}
}

// TestScrubAmbient_NoColorEmptyValue pins the NO_COLOR edge: the standard says
// ANY value, even empty, disables colour (ui.resolveColor uses LookupEnv), so
// the scrub must UNSET it — setting it to "" would not neutralize it.
func TestScrubAmbient_NoColorEmptyValue(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	testenv.ScrubAmbient()
	if _, present := os.LookupEnv("NO_COLOR"); present {
		t.Fatal("NO_COLOR= (empty) must be unset by ScrubAmbient, not left present")
	}
}

// scrubProbeChildEnv marks the re-executed child of TestScrubRunsAtInit. It is
// deliberately NOT an AGENTSYNC_* name, so the scrub under test cannot clear
// the marker itself.
const scrubProbeChildEnv = "TESTENV_SCRUB_PROBE_CHILD"

// TestScrubRunsAtInit pins the WIRING, not just the function: importing
// testenv must scrub before any test body runs, with no TestMain or guard call
// involved. In a pristine environment that is unobservable from inside the
// process (there is nothing to scrub), so the test re-executes its own binary
// with the ambient variables exported and lets the child assert they are gone
// by the time its test body runs — the standard os/exec re-exec pattern. It
// is what turns "the configured CI leg would catch a regression" into "a
// pristine `go test` catches it too".
func TestScrubRunsAtInit(t *testing.T) {
	if os.Getenv(scrubProbeChildEnv) == "1" {
		// Child: init() has already run. Every ambient variable the parent
		// exported must be gone; the harness signal must survive.
		for _, k := range []string{"AGENTSYNC_HOME", "AGENTSYNC_TARGET_ROOT", "GROK_HOME", "NO_COLOR", "EDITOR"} {
			if v, present := os.LookupEnv(k); present {
				t.Errorf("%s=%q survived into the test body; testenv's init must scrub it", k, v)
			}
		}
		if os.Getenv("AGENTSYNC_TEST_DEBUG") != "kept" {
			t.Error("AGENTSYNC_TEST_DEBUG must survive the scrub (harness signal)")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestScrubRunsAtInit$", "-test.v")
	cmd.Env = append(
		os.Environ(),
		scrubProbeChildEnv+"=1",
		"AGENTSYNC_HOME=/tmp/ambient-agentsync-home",
		"AGENTSYNC_TARGET_ROOT=/tmp/ambient-target-root",
		"GROK_HOME=/tmp/ambient-grok-home",
		"NO_COLOR=1",
		"EDITOR=/bin/false",
		"AGENTSYNC_TEST_DEBUG=kept",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("re-executed child failed: %v\n%s", err, out)
	}
	// Anchor on THIS test's own PASS line: a bare "PASS" also appears when the
	// -test.run pattern matches nothing ("no tests to run"), which a rename of
	// this function would silently produce.
	if !strings.Contains(string(out), "--- PASS: TestScrubRunsAtInit") {
		t.Fatalf("child did not run and pass TestScrubRunsAtInit:\n%s", out)
	}
}
