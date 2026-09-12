package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/spf13/cobra"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// editFixture is the tree one interrupt row runs in. plainDir is where
// secretsEdit's decrypted copy lands (TMPDIR is pointed at it), and it is
// asserted EMPTY afterwards — so every helper file this test needs lives
// outside it, under root.
type editFixture struct {
	root     string
	agePath  string
	plainDir string
	workDir  string // editor scripts + markers; never inside plainDir
}

// newEditFixture builds a minimal age vault, points AGENTSYNC_HOME at it, and
// redirects TMPDIR at an otherwise-empty directory. The TMPDIR redirection is
// what makes "no cleartext left on disk" checkable at all: secretsEdit writes
// its plaintext copy with os.CreateTemp(""), which honours $TMPDIR.
func newEditFixture(t *testing.T, plaintext string) editFixture {
	t.Helper()
	testenv.RequireContainer(t)
	f := editFixture{root: t.TempDir()}
	f.plainDir = filepath.Join(f.root, "plain")
	f.workDir = filepath.Join(f.root, "work")
	home := filepath.Join(f.root, ".agentsync")
	for _, d := range []string{f.plainDir, f.workDir, filepath.Join(home, "secrets")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	idPath := filepath.Join(f.root, "age.key")
	if err := os.WriteFile(idPath, []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	f.agePath = filepath.Join(home, "secrets", "secrets.age")
	if err := secrets.Encrypt([]byte(plaintext), id.Recipient().String(), f.agePath); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[secrets]\nbackend = \"age\"\nfile = \"secrets/secrets.age\"\nrecipient = %q\nidentity_file = %q\n",
		id.Recipient().String(), idPath)
	if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTSYNC_HOME", home)
	t.Setenv("TMPDIR", f.plainDir)
	return f
}

// editor writes an executable shell script under workDir and points $EDITOR at
// it. The script's last argument is the file to edit (editorArgv appends it).
func (f editFixture) editor(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(f.workDir, "editor.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", p)
}

// marker is a path under workDir a script can touch and the seam can wait for.
func (f editFixture) marker(name string) string { return filepath.Join(f.workDir, name) }

// assertPlainDirEmpty fails if anything at all is left where the decrypted vault
// copy was written, and separately if any of it contains needle. Emptiness is
// the stronger assertion and the one the old os.Exit path could not be held to.
func (f editFixture) assertPlainDirEmpty(t *testing.T, needle string) {
	t.Helper()
	var left []string
	if err := filepath.WalkDir(f.plainDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		left = append(left, p)
		if data, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(data), needle) {
			t.Errorf("cleartext vault left on disk at %s", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%d file(s) left under TMPDIR after `secret edit`: %v — the decrypted copy must be "+
			"removed on every exit path, interrupted or not", len(left), left)
	}
}

// seamCancelNow replaces secretEditSignals with one that hands back an
// already-cancelled context — the shape of a signal that lands before the
// editor is even started.
func seamCancelNow(t *testing.T) {
	t.Helper()
	swapSeam(t, func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, cancel
	})
}

// seamCancelOnMarker replaces secretEditSignals with one that cancels as soon
// as the editor creates marker — i.e. a signal that lands WHILE the editor is
// running, deterministically, with no real signal and no sleep-based race.
func seamCancelOnMarker(t *testing.T, marker string) {
	t.Helper()
	swapSeam(t, func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		go func() {
			for {
				if _, err := os.Stat(marker); err == nil {
					cancel()
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Millisecond):
				}
			}
		}()
		return ctx, cancel
	})
}

// seamCancelWhenReadBlocks replaces secretEditSignals with one that cancels at
// the ONE point no other seam can reach: after the editor has exited and
// secretsEdit is inside os.ReadFile, before the vault is rewritten. The editor
// replaces the temp file with a FIFO (writing its path to marker) and exits;
// ReadFile then blocks opening the FIFO until a writer appears, and this seam
// is that writer. A FIFO opened for writing blocks until a reader holds it, so
// the open returning is proof that secretsEdit is already past its post-Run
// check and inside ReadFile — the seam cancels THEN feeds it a valid vault.
// Deterministic: no sleeps and no second production seam. (Should secretsEdit
// never reach ReadFile — a failing row — the goroutine stays blocked in the
// open until the test binary exits; it holds nothing the test asserts on.)
func seamCancelWhenReadBlocks(t *testing.T, marker string) {
	t.Helper()
	swapSeam(t, func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		go func() {
			var fifo string
			for fifo == "" {
				if b, err := os.ReadFile(marker); err == nil && len(b) > 0 {
					fifo = string(b)
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Millisecond):
				}
			}
			w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
			if err != nil {
				return
			}
			cancel()
			_, _ = w.WriteString("[edited]\nval = \"yes\"\n")
			_ = w.Close()
		}()
		return ctx, cancel
	})
}

// seamNeverCancels is the no-signal shape, so the happy path exercises the seam
// too rather than bypassing it.
func seamNeverCancels(t *testing.T) {
	t.Helper()
	swapSeam(t, context.WithCancel)
}

func swapSeam(t *testing.T, fn func(context.Context) (context.Context, context.CancelFunc)) {
	t.Helper()
	prev := secretEditSignals
	t.Cleanup(func() { secretEditSignals = prev })
	secretEditSignals = fn
}

func runSecretsEdit(t *testing.T) error {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	return secretsEdit(cmd, nil)
}

// TestSecretsEdit_InterruptAbandonsTheEdit is the test that could not exist
// before. `secret edit` used to handle SIGINT/SIGTERM in a goroutine that
// removed the temp file and then called os.Exit(130) directly — and an os.Exit
// from a goroutine takes the test binary with it, so nothing could assert that
// the cleartext copy was gone, that the vault was left alone, or that the exit
// code was the one it claimed. It also skipped every OTHER deferred cleanup,
// the global lock's release included, and could fire mid-re-encrypt.
//
// Every row asserts the same four things: the sentinel error comes back, it
// carries exit code 130 through ExitCoder so the root reproduces the old
// process exit code, the vault on disk is byte-unchanged, and NOTHING is left
// under TMPDIR.
//
// The "while the editor is running" row is the one that pins the no-hang
// property: its editor writes a valid replacement vault and then sleeps for a
// long time, so a `secret edit` that waited for the editor after the interrupt
// would time the test out rather than fail it — and one that saved anyway would
// fail the byte-unchanged assertion.
func TestSecretsEdit_InterruptAbandonsTheEdit(t *testing.T) {
	const secretValue = "SENTINEL_VAULT_VALUE_DO_NOT_LEAK"
	const vault = "[svc]\ntoken = \"" + secretValue + "\"\n"

	tests := []struct {
		name     string
		setup    func(t *testing.T, f editFixture)
		minTaken time.Duration // lower bound on elapsed time, for the escalation row
	}{
		{
			name: "before the editor starts",
			setup: func(t *testing.T, f editFixture) {
				// exec.CommandContext's Start fails immediately on an
				// already-cancelled context, so the editor never opens — and
				// the same post-Run check covers it.
				f.editor(t, "exit 0")
				seamCancelNow(t)
			},
		},
		{
			name: "while the editor is running",
			setup: func(t *testing.T, f editFixture) {
				// `exec` on the last line matters: without it the shell's
				// child outlives the signalled shell and keeps the test
				// binary's inherited stdout pipe open, which `go test` reports
				// as "Test I/O incomplete 30s after exiting". A real editor is
				// a single process too.
				f.editor(t, `for a in "$@"; do f="$a"; done
printf '[edited]\nval = "yes"\n' > "$f"
touch `+f.marker("opened")+`
exec sleep 600`)
				seamCancelOnMarker(t, f.marker("opened"))
			},
		},
		{
			// An editor that IGNORES the forwarded signal must not be able to wedge
			// the command. This is the row that pins exec's WaitDelay escalation:
			// the script sets TERM (and INT) to ignored, so the forward does nothing
			// and only the grace-period kill ends it. Without WaitDelay this row
			// does not fail, it HANGS — which is exactly the regression it exists to
			// catch.
			//
			// `exec sleep` keeps the editor a single process: an ignored
			// disposition survives execve (POSIX), so the `sleep` still ignores the
			// forward, and the kill leaves nothing behind holding the test binary's
			// inherited stdout pipe (which `go test` reports as "Test I/O incomplete
			// 30s after exiting"). The long duration is deliberate and load-bearing
			// — shorten it and removing WaitDelay stops being detectable, because
			// the editor would exit on its own before the assertion below could
			// notice.
			name: "an editor that ignores the interrupt is stopped after the grace period",
			setup: func(t *testing.T, f editFixture) {
				f.editor(t, `trap "" INT TERM
for a in "$@"; do f="$a"; done
touch `+f.marker("opened")+`
exec sleep 600`)
				seamCancelOnMarker(t, f.marker("opened"))
			},
			minTaken: secretEditGrace,
		},
		{
			// The window between the editor exiting and the vault being rewritten:
			// the check before WriteVerified is the only thing guarding it. No
			// signal can be placed there by hand, so the editor turns the temp file
			// into a FIFO and exits, and the seam cancels only once secretsEdit is
			// blocked reading it (see seamCancelWhenReadBlocks). Remove that check
			// and this row fails: the vault is rewritten.
			name: "after the editor exits but before the re-encrypt",
			setup: func(t *testing.T, f editFixture) {
				f.editor(t, `for a in "$@"; do f="$a"; done
rm -f "$f"
mkfifo "$f"
printf '%s' "$f" > `+f.marker("fifo.tmp")+` && mv `+f.marker("fifo.tmp")+` `+f.marker("fifo")+`
exit 0`)
				seamCancelWhenReadBlocks(t, f.marker("fifo"))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newEditFixture(t, vault)
			before, err := os.ReadFile(f.agePath)
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(t, f)

			started := time.Now()
			gotErr := runSecretsEdit(t)
			taken := time.Since(started)
			if tc.minTaken > 0 && taken < tc.minTaken {
				t.Errorf("returned after %s, want at least %s: an editor that ignores the interrupt "+
					"should have been killed by the grace-period escalation, not exited on its own",
					taken, tc.minTaken)
			}

			if !errors.Is(gotErr, errSecretEditInterrupted) {
				t.Fatalf("secretsEdit = %v, want the interrupt sentinel", gotErr)
			}
			var ec ExitCoder
			if !errors.As(gotErr, &ec) {
				t.Fatal("the interrupt sentinel does not implement ExitCoder, so the root cannot map it to 130")
			}
			if ec.ExitCode() != exitCodeInterrupted {
				t.Errorf("ExitCode() = %d, want %d (128 + SIGINT, the code the old os.Exit produced)",
					ec.ExitCode(), exitCodeInterrupted)
			}
			after, err := os.ReadFile(f.agePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Error("an interrupted `secret edit` rewrote the vault; nothing may be saved once the user has bailed")
			}
			f.assertPlainDirEmpty(t, secretValue)
		})
	}
}

// TestSecretsEdit_UninterruptedStillSaves is the other half of the pair: the
// seam must not change the happy path. Without it, the rows above would pass
// just as well against a `secret edit` that refused every edit.
func TestSecretsEdit_UninterruptedStillSaves(t *testing.T) {
	const secretValue = "SENTINEL_VAULT_VALUE_DO_NOT_LEAK"
	f := newEditFixture(t, "[svc]\ntoken = \""+secretValue+"\"\n")
	before, err := os.ReadFile(f.agePath)
	if err != nil {
		t.Fatal(err)
	}
	f.editor(t, `for a in "$@"; do f="$a"; done; printf '[edited]\nval = "yes"\n' > "$f"`)
	seamNeverCancels(t)

	if err := runSecretsEdit(t); err != nil {
		t.Fatalf("secretsEdit with no interrupt: %v", err)
	}
	after, err := os.ReadFile(f.agePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == string(before) {
		t.Error("an uninterrupted `secret edit` did not rewrite the vault")
	}
	f.assertPlainDirEmpty(t, secretValue)
}
