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

func TestSecretsSet_InvalidArg(t *testing.T) {
	env, _, _, _ := setupSecretsEnv(t)
	_, err := runCLI(t, env, "secrets", "set", "noequalssign")
	if err == nil {
		t.Fatal("expected error for missing = in set argument")
	}
}
