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
)

// TestCheckIdentityPermissionsShape pins the SHAPE arm of the shared pre-read
// gate on [secrets].identity_file — the arm that keeps a non-regular file from
// ever reaching os.ReadFile.
//
// Why it lives on the gate and not only on the validator: secrets.ValidateConfig
// has had a regular-file rule since the round-2 pass, but nothing except
// `agentsync check` runs it before reading. `secrets.RequireAgeVault` — the gate
// the whole `secret` group passes through — only tests config keys for
// non-emptiness, and AgeBackend.load puts the same read behind `apply` and
// behind doctor's checkSecretReferences. Measured against a fixture with a real
// vault, a live ${secret:…} reference and a valid recipient, a 0600 FIFO at
// identity_file wedged `secret get`, `secret list`, `secret set`, `secret edit`,
// `secret remove`, `apply --dry-run` and `doctor` (the last after printing its
// own ✗ for that very file) until each was killed; only `check` was clean.
// CheckIdentityPermissions is the one function all of those already call.
//
// Every assertion here is on the GATE, which only ever os.Stats — stat(2) does
// not open a FIFO. A test that instead drove a command through to the read would
// not fail, it would HANG the suite with no diagnostic. The per-case timeout is
// the other half of that argument and doubles as the guard: if this gate is ever
// changed to open the path it means to check (an os.ReadFile "shape probe", say),
// it is reported in 5s instead of wedging CI.
func TestCheckIdentityPermissionsShape(t *testing.T) {
	cases := []struct {
		name string
		// setup returns the path to gate. It must never OPEN that path.
		setup func(t *testing.T, tmp string) string
		// skipPerm sets AGENTSYNC_AGE_SKIP_PERM_CHECK=1 for the case.
		skipPerm     bool
		wantErr      bool
		wantContains string
	}{
		{
			// A FIFO clears stat AND, at 0600, the permission gate: only a
			// regular-file rule can catch it. Its read does not even fail —
			// os.ReadFile waits for a writer that never comes.
			name:         "a FIFO identity is rejected by shape",
			setup:        mkfifoIdentity,
			wantErr:      true,
			wantContains: "not a regular file",
		},
		{
			// The placement row. AGENTSYNC_AGE_SKIP_PERM_CHECK exists for setups
			// where MODE BITS are meaningless (NFS root-squash, ACLs) — a FIFO is
			// a FIFO regardless. If the shape arm is moved below this override,
			// every user who sets it gets the unkillable hang back, which is the
			// worst possible audience for that regression.
			name:         "the permission-check override does not disable the shape rule",
			setup:        mkfifoIdentity,
			skipPerm:     true,
			wantErr:      true,
			wantContains: "not a regular file",
		},
		{
			// A 0700 directory has mode&0o077 == 0, so it clears the permission
			// rule too; the shape rule is the only thing between it and
			// `read identity …: is a directory` at decrypt time.
			name: "a directory identity is rejected by shape, not by permissions",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				p := filepath.Join(tmp, "age.key")
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErr:      true,
			wantContains: "not a regular file",
		},
		{
			// The guard against an over-broad arm: the ordinary identity — a
			// 0600 regular file — must still pass the gate untouched.
			name: "a 0600 regular identity still passes",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				p := filepath.Join(tmp, "age.key")
				if err := os.WriteFile(p, []byte("AGE-SECRET-KEY-fake"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(p, 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Pin the override explicitly in BOTH directions: an ambient
			// AGENTSYNC_AGE_SKIP_PERM_CHECK=1 must not decide which arm answers.
			if tc.skipPerm {
				t.Setenv(secrets.SkipPermCheckEnv, "1")
			} else {
				t.Setenv(secrets.SkipPermCheckEnv, "")
			}
			path := tc.setup(t, t.TempDir())

			// The result travels by channel rather than a captured variable so a
			// gate that DID block cannot race the assertions under -race.
			errCh := make(chan error, 1)
			go func() { errCh <- secrets.CheckIdentityPermissions(path) }()
			var err error
			select {
			case err = <-errCh:
			case <-time.After(5 * time.Second):
				t.Fatalf("CheckIdentityPermissions BLOCKED on %s — the gate must STAT the "+
					"path, never open it: os.ReadFile does not fail on a FIFO, it waits for "+
					"a writer that never comes", path)
			}

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("CheckIdentityPermissions(%s) = %v, want nil", path, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckIdentityPermissions(%s) = nil, want a shape error: this path "+
					"is handed to os.ReadFile by AgeBackend.load and Decrypt", path)
			}
			if !strings.Contains(err.Error(), tc.wantContains) {
				t.Errorf("error = %q, want it to contain %q (the validator's wording, so the "+
					"gate and the report say the same thing about the same file)",
					err.Error(), tc.wantContains)
			}
		})
	}
}

// mkfifoIdentity creates a 0600 FIFO and returns its path. mkfifo and chmod do
// not open the FIFO; nothing in this file ever does.
func mkfifoIdentity(t *testing.T, tmp string) string {
	t.Helper()
	p := filepath.Join(tmp, "age.key")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	// mkfifo applies the process umask; chmod is what pins 0600, so a FIFO row
	// cannot be answered by the "too permissive" arm instead of the shape one.
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
