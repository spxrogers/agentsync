package secrets_test

import (
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestVaultLoad_EmptyVaultYieldsWritableMap pins Load's nil-map guard. A vault
// that decrypts to an empty TOML document (a user emptied it in `secret edit`,
// or only comments remain) unmarshals to a NIL map, and the very next
// `secret set` assigns into it: without the guard that is a panic, not a
// refusal. The guard is one line and nothing else in the suite reaches it.
func TestVaultLoad_EmptyVaultYieldsWritableMap(t *testing.T) {
	testenv.RequireContainer(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	idPath := filepath.Join(home, "identity.txt")
	if err := os.WriteFile(idPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := source.SecretsConfig{
		Backend:      "age",
		Recipient:    id.Recipient().String(),
		File:         "secrets/secrets.age",
		IdentityFile: idPath,
	}
	v := secrets.NewVault(cfg, home, "")
	if err := secrets.Encrypt([]byte("# nothing left\n"), cfg.Recipient, v.AgeFile()); err != nil {
		t.Fatal(err)
	}

	m, err := v.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m == nil {
		t.Fatal("Load returned a nil map for an empty vault; the next set would panic")
	}
	// The map must be assignable — this is the line that panics without the guard.
	if err := secrets.SetNestedKey(m, "probe.key", "value"); err != nil {
		t.Fatalf("SetNestedKey on a freshly loaded empty vault: %v", err)
	}
	if err := v.Save(m); err != nil {
		t.Fatalf("Save after the first key: %v", err)
	}
	back, err := v.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := secrets.GetNestedKey(back, "probe.key"); !ok || got != "value" {
		t.Fatalf("round trip after an empty vault: got %v, %v", got, ok)
	}
}

// TestVaultWriteVerified_RollsBackToNothingWhenNoVaultExisted pins the arm of
// the rollback that TestSecretsSet_RejectsRecipientIdentityMismatch cannot: a
// FIRST write whose recipient the identity cannot read back. There is no
// previous store to restore, so the just-written file must be removed rather
// than left behind as a vault nobody can open — "refusing to lock you out"
// means no vault, not a locked one.
func TestVaultWriteVerified_RollsBackToNothingWhenNoVaultExisted(t *testing.T) {
	testenv.RequireContainer(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	idPath := filepath.Join(home, "identity.txt")
	if err := os.WriteFile(idPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Recipient is a DIFFERENT key from the identity: the encrypt succeeds and
	// the verify decrypt fails, which is the lock-out this arm exists for.
	cfg := source.SecretsConfig{
		Backend:      "age",
		Recipient:    other.Recipient().String(),
		File:         "secrets/secrets.age",
		IdentityFile: idPath,
	}
	v := secrets.NewVault(cfg, home, "")
	if _, err := os.Stat(v.AgeFile()); !os.IsNotExist(err) {
		t.Fatalf("precondition: no vault yet, got %v", err)
	}

	err = v.WriteVerified([]byte("[a]\nb = \"c\"\n"))
	if err == nil {
		t.Fatal("a store the identity cannot decrypt must be refused")
	}
	if _, statErr := os.Stat(v.AgeFile()); !os.IsNotExist(statErr) {
		t.Fatalf("the unreadable vault must be removed, not left behind: stat = %v", statErr)
	}
}
