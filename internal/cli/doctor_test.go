package cli_test

import (
	"os"
	"path/filepath"
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
	for _, want := range []string{
		"AGENTSYNC_HOME", "Go version", "OS",
		"home dir   ok", ".state/    ok", "schema     ok",
		"claude", "opencode",
		"Destination git backup", "mode       prompt",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q. Got:\n%s", want, out)
		}
	}
}

// TestDoctor_FailsOnMissingHome asserts that doctor exits non-zero when
// the user runs it before `agentsync init`. The old PATH-only doctor
// returned ok no matter what.
func TestDoctor_FailsOnMissingHome(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	out, err := runCLI(t, env, "doctor")
	if err == nil {
		t.Fatalf("doctor should fail on missing home; got:\n%s", out)
	}
	if !strings.Contains(out, "agentsync init") {
		t.Fatalf("doctor should suggest `agentsync init` on missing home; got:\n%s", out)
	}
}

// TestDoctor_FailsOnBadIdentityPerms asserts that a too-permissive age
// identity file fails the readiness check.
// TestDoctor_HonorsSkipPermCheck is the regression for doctor's inline 0o077
// identity-perm check ignoring AGENTSYNC_AGE_SKIP_PERM_CHECK=1, which apply and
// verify both honor (via secrets.CheckIdentityPermissions). doctor would falsely
// fail a 0644 identity even when the user opted out of the perm gate.
func TestDoctor_HonorsSkipPermCheck(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT":         tmp,
		"HOME":                          tmp,
		"AGENTSYNC_AGE_SKIP_PERM_CHECK": "1",
	}
	_, _ = runCLI(t, env, "init")

	identity := filepath.Join(tmp, "age.key")
	if err := os.WriteFile(identity, []byte("# fake key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body := `[agents]
[secrets]
backend       = "age"
recipient     = "age1qqqq"
identity_file = "` + identity + `"
file          = "secrets/secrets.age"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor should pass with AGENTSYNC_AGE_SKIP_PERM_CHECK=1; got err=%v\n%s", err, out)
	}
	if strings.Contains(out, "too permissive") {
		t.Fatalf("doctor must honor the perm-skip override; got:\n%s", out)
	}
}

func TestDoctor_FailsOnBadIdentityPerms(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp}
	_, _ = runCLI(t, env, "init")

	identity := filepath.Join(tmp, "age.key")
	if err := os.WriteFile(identity, []byte("# fake key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body := `[agents]
[secrets]
backend       = "age"
recipient     = "age1qqqq"
identity_file = "` + identity + `"
file          = "secrets/secrets.age"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "doctor")
	if err == nil {
		t.Fatalf("doctor should fail on 0644 identity; got:\n%s", out)
	}
	if !strings.Contains(out, "too permissive") {
		t.Fatalf("doctor should warn about identity perms; got:\n%s", out)
	}
}
