//go:build unix

package secrets_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestWriteSecretsVerifiedSurvivesNonRegularVault pins the vault read that has
// no other gate in front of it: Vault.WriteVerified's rollback snapshot. (It
// kept its name through the move from internal/cli, where the method was the
// package-level writeSecretsVerified.)
//
// It used to be a bare os.ReadFile, and os.ReadFile blocks on a FIFO exactly as
// os.Open does. The reason no earlier gate saved it is the reason this test
// exists at all: `secret edit` stats the vault first and, when it is ABSENT,
// takes a branch that never decrypts — so the shape gate on the decrypt path is
// simply not on this route. Measured before the fix, an $EDITOR that created a
// FIFO at the vault path during its own edit window wedged `secret edit`
// deterministically (rc=124), while an identical run whose editor left the path
// alone exited 0. That window is exactly the state the vault gate's
// stat-fallthrough is designed to permit, which is what makes this the reachable
// case rather than a theoretical one.
//
// The timeout is the assertion. A regression here does not fail, it HANGS, so a
// plain call would wedge CI with no diagnostic instead of reporting in 5s.
func TestWriteSecretsVerifiedSurvivesNonRegularVault(t *testing.T) {
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
	vault := v.AgeFile()

	// The FIFO stands in for whatever put a non-regular file at the vault path
	// during the edit window. It is never opened here: mkfifo and chmod do not
	// open it, and if the code under test does, the timeout below reports that.
	if err := syscall.Mkfifo(vault, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	if err := os.Chmod(vault, 0o600); err != nil {
		t.Fatal(err)
	}

	// By channel, not a captured variable, so a call that DID block cannot race
	// the assertions under -race.
	errCh := make(chan error, 1)
	go func() {
		errCh <- v.WriteVerified([]byte("[demo]\nkey = \"v\"\n"))
	}()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("WriteVerified = %v, want success: a non-regular vault carries "+
				"no previous secrets to preserve, so the snapshot reports none and the "+
				"encrypt replaces the path atomically", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("WriteVerified BLOCKED on %s — the rollback snapshot must go "+
			"through the shape gate: os.ReadFile waits for a writer that never comes, "+
			"and `secret edit` reaches here without having decrypted", vault)
	}

	// The vault must now be a real vault, not the FIFO: proving the command
	// completed its job rather than merely declining to hang.
	info, err := os.Lstat(vault)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("vault is %s after a successful write, want a regular file", info.Mode().Type())
	}
}
