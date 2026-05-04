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

func TestApply_NoFlag_WritesDestinations_M1(t *testing.T) {
	// M1+: apply (no flag) should succeed (real adapters are wired).
	// With an empty canonical there are 0 ops, so no files are written, but
	// the command must not error.
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "agent", "add", "claude")

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply in M1 should succeed; got err: %v\n%s", err, out)
	}
}
