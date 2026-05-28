package cli_test

import (
	"strings"
	"testing"
)

// TestColorFlag locks in the --color flag's three modes:
//   - auto (the default) on a non-TTY pipe → no color, so captured tests stay
//     byte-stable and piped output never leaks raw ANSI.
//   - always forces color even off a terminal.
//   - never disables color even when the flag's auto path would have enabled it.
//
// We probe via `doctor`, which always renders glyphs and (under color) wraps
// the status token in ANSI; --version on its own emits no styled tokens.
func TestColorFlag(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	// auto + non-TTY → plain
	autoOut, _ := runCLI(t, env, "doctor")
	if strings.Contains(autoOut, "\x1b[") {
		t.Fatalf("auto color on a captured (non-TTY) buffer must produce no ANSI; got:\n%s", autoOut)
	}

	// always forces color, even off a terminal.
	alwaysOut, _ := runCLI(t, env, "doctor", "--color=always")
	if !strings.Contains(alwaysOut, "\x1b[") {
		t.Fatalf("--color=always should emit ANSI even on a non-TTY; got plain:\n%s", alwaysOut)
	}

	// never disables color unconditionally.
	neverOut, _ := runCLI(t, env, "doctor", "--color=never")
	if strings.Contains(neverOut, "\x1b[") {
		t.Fatalf("--color=never must emit no ANSI; got:\n%s", neverOut)
	}

	// Bogus mode value reports a clear error.
	bogusOut, err := runCLI(t, env, "doctor", "--color=psychedelic")
	if err == nil {
		t.Fatalf("--color=psychedelic should error; got success:\n%s", bogusOut)
	}
}
