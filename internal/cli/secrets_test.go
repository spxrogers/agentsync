package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	secrets_pkg "github.com/spxrogers/agentsync/internal/secrets"
)

// setupSecretsEnv initialises a fresh agentsync home with age [secrets] config,
// returning the env map and paths to the age file + identity file.
func setupSecretsEnv(t *testing.T) (env map[string]string, agePath, idPath string, id *age.X25519Identity) {
	t.Helper()
	tmp := t.TempDir()
	env = map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	// Generate age identity.
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	idPath = filepath.Join(tmp, ".config", "agentsync", "age.key")
	if err := os.MkdirAll(filepath.Dir(idPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idPath, []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	// Append [secrets] block to agentsync.toml.
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	existing, _ := os.ReadFile(cfgPath)
	block := fmt.Sprintf("\n[secrets]\nbackend = \"age\"\nfile = \"secrets/secrets.age\"\nrecipient = %q\nidentity_file = %q\n",
		id.Recipient().String(), idPath)
	if err := os.WriteFile(cfgPath, append(existing, []byte(block)...), 0o644); err != nil {
		t.Fatal(err)
	}

	agePath = filepath.Join(tmp, ".agentsync", "secrets", "secrets.age")
	return env, agePath, idPath, id
}

func TestSecretsGetSet(t *testing.T) {
	env, agePath, idPath, id := setupSecretsEnv(t)
	_ = idPath

	// Pre-create the secrets.age file with an initial value.
	plain := []byte("[github]\ntoken = \"initial_token\"\n")
	if err := secrets_pkg.Encrypt(plain, id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}

	// get existing key
	out, err := runCLI(t, env, "secrets", "get", "github.token")
	if err != nil {
		t.Fatalf("secrets get: %v\n%s", err, out)
	}
	if !strings.Contains(out, "initial_token") {
		t.Fatalf("expected initial_token, got %q", out)
	}

	// set new key
	out, err = runCLI(t, env, "secrets", "set", "linear.api_key=lin_xyz")
	if err != nil {
		t.Fatalf("secrets set: %v\n%s", err, out)
	}

	// get newly set key
	out, err = runCLI(t, env, "secrets", "get", "linear.api_key")
	if err != nil {
		t.Fatalf("secrets get after set: %v\n%s", err, out)
	}
	if !strings.Contains(out, "lin_xyz") {
		t.Fatalf("expected lin_xyz, got %q", out)
	}

	// original key still present
	out, err = runCLI(t, env, "secrets", "get", "github.token")
	if err != nil {
		t.Fatalf("get original after set: %v\n%s", err, out)
	}
	if !strings.Contains(out, "initial_token") {
		t.Fatalf("expected initial_token still present, got %q", out)
	}
}

// TestSecretsSet_RejectsRecipientIdentityMismatch is the regression for
// `secrets set` re-encrypting the whole store to a recipient the configured
// identity_file cannot decrypt — silently locking the user out of their own
// secrets. It must refuse and leave the existing store intact.
func TestSecretsSet_RejectsRecipientIdentityMismatch(t *testing.T) {
	env, agePath, _, id := setupSecretsEnv(t)
	// Seed a store readable by the configured identity.
	if err := secrets_pkg.Encrypt([]byte("[a]\nb = \"c\"\n"), id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}
	// Point recipient at a DIFFERENT key while leaving identity_file unchanged.
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(env["AGENTSYNC_TARGET_ROOT"], ".agentsync", "agentsync.toml")
	data, _ := os.ReadFile(cfgPath)
	mismatched := strings.Replace(string(data), id.Recipient().String(), other.Recipient().String(), 1)
	if err := os.WriteFile(cfgPath, []byte(mismatched), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "secrets", "set", "x.y=z"); err == nil {
		t.Fatal("secrets set with a recipient the identity can't decrypt should be rejected")
	}
	// The original store must still be readable (rolled back, not clobbered).
	out, gerr := runCLI(t, env, "secrets", "get", "a.b")
	if gerr != nil || !strings.Contains(out, "c") {
		t.Fatalf("original store not preserved after rejected set: err=%v out=%q", gerr, out)
	}
}

func TestSecretsGet_MissingKey(t *testing.T) {
	env, agePath, _, id := setupSecretsEnv(t)
	plain := []byte("[foo]\nbar = \"baz\"\n")
	if err := secrets_pkg.Encrypt(plain, id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "secrets", "get", "nonexistent.key")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

// TestSecretsSet_NoEqualsNonTTYErrors is the regression for the bug where
// `agentsync secrets set <something>` (no `=`) printed the user's argument
// back via `got %q` — leaking it if the argument was a real token. The
// new behaviour refuses without echoing the argument when stdin is not a
// TTY (the test environment).
func TestSecretsSet_NoEqualsNonTTYErrors(t *testing.T) {
	const sentinel = "ghp_SENTINEL_NEVER_ECHO_THIS_TOKEN"
	env, _, _, _ := setupSecretsEnv(t)
	out, err := runCLI(t, env, "secrets", "set", sentinel)
	if err == nil {
		t.Fatal("expected error when no '=' and stdin is not a terminal")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("SECURITY: error message echoed sentinel %q: %v", sentinel, err)
	}
	if strings.Contains(out, sentinel) {
		t.Fatalf("SECURITY: stdout/stderr echoed sentinel %q:\n%s", sentinel, out)
	}
}

// TestSecretsSet_StdinPath proves the safe input mode works end-to-end:
// the secret never appears on argv (so ps(1) / history don't see it) and
// the stored value matches the stdin payload.
func TestSecretsSet_StdinPath(t *testing.T) {
	env, _, _, _ := setupSecretsEnv(t)
	const value = "ghp_VIA_STDIN_FLOW_123"
	out, err := runCLIWithStdin(t, env, value+"\n", "secrets", "set", "github.token", "--stdin")
	if err != nil {
		t.Fatalf("secrets set --stdin: %v\n%s", err, out)
	}
	got, err := runCLI(t, env, "secrets", "get", "github.token")
	if err != nil {
		t.Fatalf("get after stdin set: %v\n%s", err, got)
	}
	if !strings.Contains(got, value) {
		t.Fatalf("stored value mismatch: want %q in %q", value, got)
	}
}

// TestSecretsSet_LegacyArgWarns proves the back-compat path still works
// but warns the user that the value just hit argv.
func TestSecretsSet_LegacyArgWarns(t *testing.T) {
	env, _, _, _ := setupSecretsEnv(t)
	out, err := runCLI(t, env, "secrets", "set", "legacy.key=legacy_value")
	if err != nil {
		t.Fatalf("legacy set: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warning") || !strings.Contains(out, "--stdin") {
		t.Fatalf("legacy form did not warn about argv exposure; got:\n%s", out)
	}
}
