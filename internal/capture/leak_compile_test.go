package capture_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestResolvedIsNotWritableToSource is the Part C guard: it compiles
// leak_fixture.go (which tries to hand a secrets.Resolved to source.WriteMCP and
// to Capture) under -tags=leakfixture and asserts the build FAILS with a type
// error. A green build here would mean the resolved apply model became
// assignable to the dest->source write path again — the leak class the type
// split exists to forbid. This makes the guarantee a compile error, not a
// code-review catch.
func TestResolvedIsNotWritableToSource(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; cannot run the negative-compile fixture")
	}
	cmd := exec.Command("go", "build", "-tags=leakfixture", "./internal/capture/")
	cmd.Dir = "../.." // repo root, relative to internal/capture
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("leak_fixture.go compiled, but a secrets.Resolved MUST NOT be assignable "+
			"to source.WriteMCP / Capture; the type wall has a hole.\nbuild output:\n%s", out)
	}
	// Confirm the failure is the expected type mismatch, not an unrelated build
	// break (missing dep, syntax error elsewhere, etc.).
	got := string(out)
	if !strings.Contains(got, "secrets.Resolved") {
		t.Fatalf("expected a type error mentioning secrets.Resolved; got:\n%s", got)
	}
}
