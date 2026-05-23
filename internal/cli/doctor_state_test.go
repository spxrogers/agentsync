package cli_test

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDoctor_DetectsCorruptState is the regression for doctor's false-OK: a
// corrupt .state/targets.json makes status/apply/diff/reconcile all exit 1,
// but doctor only write-probed the .state/ directory and never loaded the
// state file — so it printed "all checks passed" (exit 0). A readiness command
// must not report healthy when every real command is broken.
func TestDoctor_DetectsCorruptState(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	if err := os.WriteFile(statePath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "doctor"); err == nil {
		t.Fatal("doctor reported healthy on a corrupt state file (should exit nonzero)")
	}
}
