//go:build unix

package secrets_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"

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

// mkfifoIdentity creates a 0600 FIFO at the identity path.
func mkfifoIdentity(t *testing.T, tmp string) string {
	t.Helper()
	return mkfifoAt(t, filepath.Join(tmp, "age.key"))
}

// TestVaultShapeGate pins the SHAPE arm on the OTHER age path — the
// [secrets].file vault that AgeBackend.load and secrets.Decrypt open.
//
// It is the exact twin of TestCheckIdentityPermissionsShape, one field over,
// and it exists because the identity fix did not cover it: open(2) on a FIFO
// blocks in the OPEN, so `open age file %s: %w` never runs. Measured against
// this fixture's shape at the commit before the fix, a 0600 FIFO at the vault
// path wedged `secret list` and `secret get` with zero output until they were
// killed, while `agentsync check` was clean — the identity symptom verbatim.
//
// THE REAL KEYPAIR BELOW IS LOAD-BEARING, not scene-setting. Every entry point
// reads and PARSES the identity before it ever opens the vault, so a fixture
// with a placeholder identity dies at age.ParseIdentities two statements early
// and never reaches the gate under test. Inside this test that failure is LOUD
// — each row misses its wantErrContains and fails — but it is exactly how the
// same mistake goes SILENT outside one: a hand probe of this scenario, run with
// no assertions at all, reported a clean exit and made the bug look
// unreproducible for a whole round. The assertions are what turn the trap into
// a failure. Keep them, and keep the real key.
//
// As in the sibling test, the per-case timeout is the guard: if the gate is
// ever changed to open the path it means to check, this reports in 5s instead
// of wedging CI with no diagnostic.
func TestVaultShapeGate(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		// setup returns the vault path to gate. It must never OPEN that path.
		setup func(t *testing.T, tmp string) string
		// wantErrContains is asserted on the error from BOTH entry points; an
		// empty value means the row must SUCCEED and yield the fixture secret.
		wantErrContains string
		// wantErrLacks guards against an over-broad arm answering a row that
		// belongs to a different one.
		wantErrLacks string
	}{
		{
			// The sharp one. A FIFO clears stat, and its open never returns.
			name:            "a FIFO vault is rejected by shape",
			setup:           mkfifoVault,
			wantErrContains: "not a regular file",
		},
		{
			// A directory does fail the open (EISDIR), so this row is about the
			// DIAGNOSIS: "is a directory" surfaces from age's stream parser
			// several layers down, "not a regular file" names the actual defect.
			name: "a directory vault is rejected by shape",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				p := filepath.Join(tmp, "secrets.age")
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErrContains: "not a regular file",
		},
		{
			// The fail-open row, and the reason the gate stats rather than
			// refusing on any stat error: an ABSENT vault is the ordinary state
			// of an install that has not run `secret set` yet, and must keep
			// reporting itself as absent rather than as the wrong shape.
			name: "an absent vault still reports the truthful open error",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				return filepath.Join(tmp, "secrets.age")
			},
			wantErrContains: "open age file",
			wantErrLacks:    "not a regular file",
		},
		{
			// The guard against an over-broad arm: an ordinary vault must still
			// decrypt end to end. This row fails if the gate rejects, if it
			// consumes the stream before age sees it, or if it leaks the handle.
			name: "an ordinary regular vault still decrypts",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				return writeVault(t, tmp, id.Recipient())
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			idPath := filepath.Join(tmp, "age.key")
			if err := os.WriteFile(idPath, []byte(id.String()+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			vault := tc.setup(t, tmp)

			// Both read paths are asserted: Decrypt and AgeBackend.load are
			// separate copies of the same open, so pinning one would leave the
			// other free to regress silently.
			entries := map[string]func() error{
				"secrets.Decrypt": func() error {
					raw, err := secrets.Decrypt(vault, idPath)
					if err == nil && !strings.Contains(string(raw), "s3cr3t") {
						return fmt.Errorf("decrypted %q, want the fixture secret", raw)
					}
					return err
				},
				// The cross-package read. internal/cli's rollback snapshot
				// calls it, and that caller is reachable with a non-regular
				// vault (see TestWriteSecretsVerifiedSurvivesNonRegularVault).
				"secrets.ReadVault": func() error {
					raw, err := secrets.ReadVault(vault)
					if err == nil && len(raw) == 0 {
						return fmt.Errorf("read 0 bytes, want the encrypted vault")
					}
					return err
				},
				"AgeBackend.Resolve": func() error {
					got, err := secrets.NewAgeBackend(vault, idPath).Resolve("demo.key")
					if err == nil && got != "s3cr3t" {
						return fmt.Errorf("resolved %q, want %q", got, "s3cr3t")
					}
					return err
				},
			}

			for name, run := range entries {
				t.Run(name, func(t *testing.T) {
					// By channel, not a captured variable, so an entry point
					// that DID block cannot race the assertions under -race.
					errCh := make(chan error, 1)
					go func() { errCh <- run() }()
					var err error
					select {
					case err = <-errCh:
					case <-time.After(5 * time.Second):
						t.Fatalf("%s BLOCKED on %s — the vault must be STAT'd for shape "+
							"before it is opened: open(2) on a FIFO waits for a writer "+
							"that never comes, so the open's own error path never runs",
							name, vault)
					}

					if tc.wantErrContains == "" {
						if err != nil {
							t.Fatalf("%s = %v, want success", name, err)
						}
						return
					}
					if err == nil {
						t.Fatalf("%s(%s) = nil, want an error containing %q",
							name, vault, tc.wantErrContains)
					}
					if !strings.Contains(err.Error(), tc.wantErrContains) {
						t.Errorf("%s error = %q, want it to contain %q",
							name, err.Error(), tc.wantErrContains)
					}
					if tc.wantErrLacks != "" && strings.Contains(err.Error(), tc.wantErrLacks) {
						t.Errorf("%s error = %q, want it NOT to contain %q",
							name, err.Error(), tc.wantErrLacks)
					}
				})
			}
		})
	}
}

// mkfifoVault creates a 0600 FIFO at the vault path.
func mkfifoVault(t *testing.T, tmp string) string {
	t.Helper()
	return mkfifoAt(t, filepath.Join(tmp, "secrets.age"))
}

// mkfifoAt creates a 0600 FIFO at path and returns it. mkfifo and chmod do not
// open the FIFO; nothing in this package ever does — a test that opened one
// would not fail, it would wedge the suite.
//
// The chmod is not redundant: mkfifo applies the process umask, so without it a
// FIFO row could be answered by a "too permissive" arm instead of the shape one
// it is written to exercise.
func mkfifoAt(t *testing.T, path string) string {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeVault encrypts the fixture secret to recipient and returns its path.
// The vault is written UNARMORED because that is what secrets.Encrypt writes
// and what age.Decrypt reads here; an armored file fails at header parsing.
func writeVault(t *testing.T, tmp string, recipient age.Recipient) string {
	t.Helper()
	p := filepath.Join(tmp, "secrets.age")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w, err := age.Encrypt(f, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "[demo]\nkey = \"s3cr3t\"\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}
