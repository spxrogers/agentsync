//go:build unix

package secrets_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestValidateConfigFIFOIdentity is the FIFO twin of TestValidateConfig's
// "directory at the identity path" row, kept in its own unix-only file because
// syscall.Mkfifo is undefined on Windows.
//
// A FIFO is the sharp shape for [secrets].identity_file. Like a directory it
// STATS fine — so before validateIdentity's regular-file arm it reported
// `✓ identity   ok` on doctor and exited 0 on check — but unlike a directory it
// does not fail the eventual read either: os.ReadFile on a FIFO with no writer
// BLOCKS FOREVER, and internal/secrets/age.go reads the identity through
// os.ReadFile in both AgeBackend.load and Decrypt.
//
// It asserts at the VALIDATOR level on purpose, rather than through `doctor` /
// `check` the way TestSecretsValidationParity's directory rows do. ValidateConfig
// only ever os.Stats these paths, and stat(2) does not open a FIFO; a test that
// drove a command which went on to READ the identity would not fail, it would
// HANG the whole suite with no diagnostic. The timeout below is the second half
// of that argument, and doubles as the guard: if validateIdentity is ever
// changed to open the path it means to check, this test reports it in 5s instead
// of wedging CI.
func TestValidateConfigFIFOIdentity(t *testing.T) {
	// The perm gate reads this env var, so pin it: an ambient
	// AGENTSYNC_AGE_SKIP_PERM_CHECK=1 must not change which arm answers.
	t.Setenv(secrets.SkipPermCheckEnv, "")
	userHome := t.TempDir()
	home := filepath.Join(userHome, ".agentsync")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	idPath := filepath.Join(userHome, "age.key")
	if err := syscall.Mkfifo(idPath, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	// mkfifo applies the process umask; chmod is what pins 0600, so this row
	// cannot be answered by the "too permissive" arm instead of the shape one.
	// chmod does not open the FIFO.
	if err := os.Chmod(idPath, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := source.SecretsConfig{Backend: "age", Recipient: "age1qqqq", IdentityFile: idPath}

	var got []secrets.Finding
	done := make(chan struct{})
	go func() { defer close(done); got = secrets.ValidateConfig(cfg, home, userHome) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ValidateConfig BLOCKED on a FIFO at [secrets].identity_file — it must " +
			"stat the path, never open it: os.ReadFile does not fail on a FIFO, it waits " +
			"for a writer that never comes")
	}

	f := findingFor(t, got, secrets.FieldIdentityFile)
	if f.Severity != secrets.SeverityFail {
		t.Errorf("identity_file severity = %s, want %s — a FIFO stats fine and at 0600 "+
			"clears the permission gate, so only a regular-file rule can catch it "+
			"(message: %s)", f.Severity, secrets.SeverityFail, f.Message)
	}
	if !strings.Contains(f.Message.String(), "not a regular file") {
		t.Errorf("identity_file message = %q, want it to contain %q",
			f.Message.String(), "not a regular file")
	}
}
