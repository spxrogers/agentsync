package cli_test

import (
	"strings"
	"testing"
)

func TestDoctor_PrintsEnvAndAgents(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")

	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for _, want := range []string{"AGENTSYNC_HOME", "Go version", "OS", "claude", "opencode"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q. Got:\n%s", want, out)
		}
	}
}
