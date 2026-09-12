package secrets_test

import (
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// newTestVault lays out an agentsync home with id written to an identity file
// and an empty secrets/ dir, and binds a Vault to it that encrypts to
// recipient. Pass id.Recipient() for a vault the identity can read back, or
// another key's recipient to make WriteVerified's verify step refuse.
func newTestVault(t *testing.T, id *age.X25519Identity, recipient *age.X25519Recipient) secrets.Vault {
	t.Helper()
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
		Recipient:    recipient.String(),
		File:         "secrets/secrets.age",
		IdentityFile: idPath,
	}
	return secrets.NewVault(cfg, home, "")
}
