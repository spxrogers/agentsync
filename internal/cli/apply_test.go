package cli_test

import (
	"strings"
	"testing"
)

func TestApply_DryRunEmptyHome(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "agent", "add", "claude")

	out, err := runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("apply --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "claude") {
		t.Fatalf("dry-run output missing per-agent breakdown: %s", out)
	}
	if !strings.Contains(out, "0 ops") {
		t.Fatalf("dry-run should report 0 ops on empty canonical: %s", out)
	}
}

func TestApply_NoFlagDefaultsToDryRun_M0(t *testing.T) {
	// M0 NEVER writes destinations; flag the user when they call apply
	// without --dry-run so they understand M0 vs M1+ scope.
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "agent", "add", "claude")

	out, err := runCLI(t, env, "apply")
	if err == nil {
		t.Fatalf("apply without --dry-run in M0 should error or warn; got: %s", out)
	}
}
