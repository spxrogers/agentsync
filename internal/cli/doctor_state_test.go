package cli_test

import (
	"os"
	"path/filepath"
	"strings"
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

// TestDoctor_SanitizesCorruptStateDiagnostic pins the print boundary for the
// schema-2 migration refusal.
//
// The refusal names each unreadable key by interpolating the map key read
// VERBATIM from targets.json (migrate's `bad` list) — and the remedy that error
// carries explicitly contemplates the user hand-editing that file. A key holding
// a terminal escape would therefore reach the terminal raw: the #93/#171 class,
// where crafted config repaints the screen or forges a passing check line.
//
// Every other state.Load error in the CLI is returned and printed by
// reportErrorTo, which sanitizes per line. doctor prints its checks inline, so
// it must sanitize at its own print site; this asserts it does.
func TestDoctor_SanitizesCorruptStateDiagnostic(t *testing.T) {
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
	// A v1 key whose SCOPE field carries an ESC. "\u001b" is how JSON must spell a
	// control byte, so json.Unmarshal hands migrate a map key holding a real one.
	// "[2J" (clear screen) is used rather than a color code so the assertion below
	// cannot collide with the printer's own styling.
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	const corrupt = `{"schema_version":1,"files":{"claude:no\u001b[2Jpe::x":{"sha256":"a"}}}`
	if err := os.WriteFile(statePath, []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "doctor")
	if err == nil {
		t.Fatal("doctor must fail on a state file it cannot load")
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Fatalf("doctor echoed a raw terminal escape from targets.json:\n%s", out)
	}
	// The key is still NAMED — sanitizing strips the escape, it does not hide the
	// diagnostic.
	if !strings.Contains(out, "[2Jpe") {
		t.Fatalf("doctor must still name the offending key (escape stripped):\n%s", out)
	}
}
