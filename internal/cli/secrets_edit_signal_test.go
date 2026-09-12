package cli_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/cli"
	secrets_pkg "github.com/spxrogers/agentsync/internal/secrets"
)

// TestSecretsEdit_RealSIGINTExitsThroughTheNormalPath drives the PRODUCTION
// signal wiring — signal.NotifyContext, no seam swapped — by having the fake
// editor send a real SIGINT to its parent, which is this test process.
//
// It is deliberately separate from the seam-driven table in
// secrets_edit_interrupt_internal_test.go: that table pins the BEHAVIOUR at each
// point of the window, this pins that the production arming is actually hooked
// up. Sending the signal is safe and race-free because secretsEdit arms
// notification BEFORE it launches the editor, so the default "terminate"
// disposition is already suppressed by the time the editor can fire; and the
// signal is targeted at $PPID, so nothing else in the process tree sees it.
//
// Before this change the handler was a goroutine that called os.Exit(130): this
// test could not have been written at all — the os.Exit would have taken the
// test binary down mid-run, reported as a hard exit with no failure attributed
// to any test.
func TestSecretsEdit_RealSIGINTExitsThroughTheNormalPath(t *testing.T) {
	const secretValue = "SENTINEL_REAL_SIGNAL_VALUE"
	env, agePath, _, id := setupSecretsEnv(t)
	if err := secrets_pkg.Encrypt([]byte("[svc]\ntoken = \""+secretValue+"\"\n"), id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(agePath)
	if err != nil {
		t.Fatal(err)
	}

	// Both dirs are created BEFORE $TMPDIR is redirected, so t.TempDir() does
	// not place them inside the directory asserted empty below.
	work := t.TempDir()
	plain := t.TempDir()
	edScript := filepath.Join(work, "editor.sh")
	// `kill -INT $PPID` targets the agentsync process (this test binary; runCLI
	// runs the command tree in process). `exec` on the last line keeps the
	// script a single process, so signalling it leaves nothing behind holding
	// the test binary's inherited stdout pipe.
	body := "#!/bin/sh\nkill -INT $PPID\nexec sleep 600\n"
	if err := os.WriteFile(edScript, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", edScript)
	t.Setenv("TMPDIR", plain)

	out, err := runCLI(t, env, "secret", "edit")
	if err == nil {
		t.Fatalf("`secret edit` interrupted by SIGINT returned no error; it must not report success\n%s", out)
	}
	var ec cli.ExitCoder
	if !errors.As(err, &ec) {
		t.Fatalf("`secret edit` returned %v, which carries no ExitCoder — the process would exit 1, "+
			"not 130 as it did before", err)
	}
	if ec.ExitCode() != 130 {
		t.Errorf("ExitCode() = %d, want 130 (128 + SIGINT)", ec.ExitCode())
	}
	// An ExitCoder is mapped by the root and its message is never printed, so
	// the interrupt stays as quiet as the old hard exit was.
	if out != "" {
		t.Errorf("an interrupted `secret edit` printed %q; the old os.Exit(130) printed nothing", out)
	}

	after, err := os.ReadFile(agePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("an interrupted `secret edit` rewrote the vault")
	}

	// The point of the whole exercise: no decrypted copy survives.
	var left []string
	if err := filepath.WalkDir(plain, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		left = append(left, p)
		if data, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(data), secretValue) {
			t.Errorf("cleartext vault left on disk at %s after SIGINT", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%d file(s) left under TMPDIR after a real SIGINT: %v", len(left), left)
	}
}
