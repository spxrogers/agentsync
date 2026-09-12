package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
	"golang.org/x/term"
)

func newSecretsCmd() *cobra.Command {
	// Singular `secret`, matching every other noun group (agent, mcp, plugin,
	// marketplace, skill, …) — #200 F4. Renamed outright, no alias.
	sec := &cobra.Command{Use: "secret", Short: "manage age-encrypted secrets", Args: cobra.NoArgs}
	sec.AddCommand(
		&cobra.Command{
			Use:   "edit",
			Short: "decrypt secrets to tmp, open $EDITOR, re-encrypt on save",
			RunE: func(cmd *cobra.Command, args []string) error {
				home := paths.AgentsyncHome(paths.OSEnv{})
				return withGlobalLock(home, func() error { return secretsEdit(cmd, args) })
			},
		},
		&cobra.Command{
			Use:   "get <key>",
			Short: "print the value of a secret key",
			Args:  cobra.ExactArgs(1),
			RunE:  secretsGet,
		},
		newSecretsSetCmd(),
		newSecretsListCmd(),
		newSecretsRemoveCmd(),
	)
	strictGroup(sec)
	markGroupScopeUnaware(sec, "the vault is per machine — secrets/secrets.age and the age identity live under "+
		"~/.agentsync, and a project tree references secrets rather than storing them")
	return sec
}

// newSecretsSetCmd builds the `secrets set` subcommand. Three argv shapes
// are supported:
//
//	secrets set <key> --stdin             # value comes from stdin
//	secrets set <key>                     # prompt with echo off (interactive)
//	secrets set <key>=<value>             # legacy; warns + recommends --stdin
//
// The legacy form leaks the value into ps(1) output, shell history, and
// auditd logs; the warning steers users to a safer mode without breaking
// existing scripts. When the argument has no '=' AND --stdin is unset AND
// stdin is not a TTY, we error out with a helpful message rather than
// hanging on a non-interactive prompt.
func newSecretsSetCmd() *cobra.Command {
	var useStdin bool
	var allowEmpty bool
	cmd := &cobra.Command{
		Use:   "set <key>[=<value>]",
		Short: "set (or update) a secret key (prompts securely by default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error {
				return secretsSet(cmd, args[0], useStdin, allowEmpty)
			})
		},
	}
	cmd.Flags().BoolVar(&useStdin, "stdin", false, "read the secret value from stdin (recommended for scripts)")
	cmd.Flags().BoolVar(&allowEmpty, "allow-empty", false, "permit storing an empty-string secret (refused by default)")
	return cmd
}

// vaultFor binds the resolved [secrets] config to a secrets.Vault. It is the
// one place the CLI supplies the user home the vault expands ${env:HOME}/~
// against, so every `secret` subcommand resolves the same two paths.
//
// The read-modify-write itself (decrypt, mutate, validate, re-encrypt, verify,
// roll back) is secrets.Vault's, not this package's: see internal/secrets/vault.go.
func vaultFor(cfg source.SecretsConfig, home string) secrets.Vault {
	return secrets.NewVault(cfg, home, paths.HomeDir(paths.OSEnv{}))
}

// loadSecretsConfig returns the SecretsConfig and the agentsync home directory.
func loadSecretsConfig() (source.SecretsConfig, string, error) {
	home := paths.AgentsyncHome(paths.OSEnv{})
	cfgPath := filepath.Join(home, "agentsync.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return source.SecretsConfig{}, home, fmt.Errorf("agentsync home not initialized (%s not found); run `agentsync init` first", cfgPath)
		}
		return source.SecretsConfig{}, home, fmt.Errorf("read %s: %w", cfgPath, err)
	}
	var cfg source.Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return source.SecretsConfig{}, home, fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	return cfg.Secrets, home, nil
}

// exitCodeInterrupted is the process exit code `secret edit` reports when a
// SIGINT/SIGTERM ends the edit: 128 + SIGINT, the shell convention for "killed
// by Ctrl-C". It is the code the old hard `os.Exit(130)` produced, preserved
// exactly — what changed is HOW it is reached.
const exitCodeInterrupted = 130

// secretEditGrace is how long a signalled editor is given to exit on its own
// before exec's WaitDelay kills it. An editor that handles the signal needs a
// moment to restore the terminal it put into raw mode; an editor that ignores
// even SIGTERM must not be able to wedge the command forever, which is what a
// bare Wait would allow. The one tunable in this path.
const secretEditGrace = 2 * time.Second

// errSecretEditInterrupted is the quiet sentinel `secret edit` returns when a
// SIGINT/SIGTERM arrives while a decrypted copy of the vault is on disk. It
// carries exit code 130 via ExitCoder, so the root maps it to the process exit
// code and prints nothing (reportErrorTo returns before any formatting) —
// byte-identical output to the hard exit it replaces.
//
// Returning an error rather than calling os.Exit is the whole point: the
// command unwinds through its deferred cleanups (the os.Remove of the cleartext
// temp file first), nothing can fire between the encrypt and the verify, the
// editor is signalled and reaped rather than orphaned on the terminal, and the
// path becomes testable at all (an os.Exit from a goroutine takes the test
// binary with it).
var errSecretEditInterrupted error = secretEditInterruptedError{}

type secretEditInterruptedError struct{}

func (secretEditInterruptedError) Error() string { return "interrupted" }
func (secretEditInterruptedError) ExitCode() int { return exitCodeInterrupted }

// commandContext returns cmd's context, or context.Background() when it has
// none. cobra's Command.Context() returns the field verbatim and only
// Execute/ExecuteContext ever sets it, so a command invoked directly — a unit
// test, or any future non-cobra caller — hands back nil, and context.WithCancel
// panics on a nil parent. Every production path goes through Execute and has
// one; this is the guard that keeps a direct call from panicking instead of
// running.
func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// secretEditSignals arms SIGINT/SIGTERM notification for the window in which a
// decrypted copy of the vault exists on disk, returning a context cancelled on
// the first such signal plus the stop function that disarms it. Injectable (the
// gitBackupPrompter / subagentMigrationPrompter precedent) so a test can drive
// the interrupt deterministically instead of signalling the test process.
var secretEditSignals = notifySecretEditSignals

// notifySecretEditSignals is the production arming: stdlib signal.NotifyContext,
// the same primitive as `signal.Notify` + a goroutine but with the cancellation
// plumbing already written. While it is armed the default disposition is
// suppressed, so the signal cannot kill the process out from under the edit.
func notifySecretEditSignals(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// editorArgv builds the editor argv from $EDITOR and the file to edit. $EDITOR
// may be empty, whitespace-only, or carry flags ("code --wait"); it is
// word-split, with a "vi" fallback when it yields no words — so the launch
// never panics indexing an empty split (the bug a bare strings.Fields had).
func editorArgv(editorEnv, file string) []string {
	parts := strings.Fields(editorEnv)
	if len(parts) == 0 {
		parts = []string{"vi"}
	}
	return append(parts, file)
}

func secretsEdit(cmd *cobra.Command, _ []string) error {
	cfg, home, err := loadSecretsConfig()
	if err != nil {
		return err
	}
	if err := secrets.RequireAgeVault(cfg, "secret edit", secrets.VaultWrite); err != nil {
		return err
	}

	vault := vaultFor(cfg, home)
	agePath := vault.AgeFile()
	var plain []byte
	if _, err := os.Stat(agePath); os.IsNotExist(err) {
		plain = []byte("# agentsync secret vault — TOML format\n# Example:\n# [github]\n# token = \"ghp_...\"\n")
	} else {
		plain, err = secrets.Decrypt(agePath, vault.IdentityFile())
		if err != nil {
			return fmt.Errorf("decrypt: %w", err)
		}
	}

	// A plain defer does NOT run when the process dies on an unhandled signal —
	// and aborting the editor with Ctrl-C (SIGINT) is the normal way to bail on
	// an edit, which would otherwise leave the decrypted secrets on disk. So arm
	// notification for the whole window in which a cleartext copy can exist:
	// from here, BEFORE the temp file is even created, until this function
	// returns. Arming first also orders the defers: LIFO runs the os.Remove
	// below BEFORE stopSignals, so a second Ctrl-C arriving while the file is
	// being removed is still caught rather than hitting the restored default
	// disposition and killing the process with the cleartext still on disk.
	//
	// This replaces a goroutine that removed the temp file and then called
	// os.Exit(130) directly. That spelling could fire mid-WriteVerified (between
	// the encrypt and the verify), left a still-running editor orphaned on the
	// terminal, and was untestable by construction — an os.Exit from a goroutine
	// takes the test binary with it, so nothing could assert the temp file was
	// gone. (The global lock is an flock the kernel releases on exit; it was
	// never at stake.)
	//
	// Now the signal cancels sigCtx. The editor runs under it, so exec signals
	// the editor and Run returns; the interrupt checks below then return
	// errSecretEditInterrupted and the command unwinds through the NORMAL error
	// path — the deferred os.Remove above runs, the editor has been signalled
	// and reaped, and the process still exits 130 because the root maps the
	// sentinel's ExitCoder.
	// (A signal that lands before the editor is even started is handled by the
	// same check: exec.CommandContext's Start fails immediately on an
	// already-cancelled context.)
	sigCtx, stopSignals := secretEditSignals(commandContext(cmd))
	defer stopSignals()
	if sigCtx.Err() != nil {
		// Already interrupted: do not put the plaintext on disk at all. The
		// checks below would abandon the edit anyway (and the deferred remove
		// would take the file), so this is a narrower cleartext window, not a
		// different outcome — which is also why no test can tell it apart.
		return errSecretEditInterrupted
	}

	// Write to a tmp file in os.TempDir() (RAM-backed on macOS).
	tmpFile, err := os.CreateTemp("", "agentsync-secrets-*.toml")
	if err != nil {
		return fmt.Errorf("create tmp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		// Always remove cleartext tmp; errors ignored.
		_ = os.Remove(tmpPath) //nolint:forbidigo // cleartext temp file in os.TempDir(), not a native destination
	}()
	if _, err := tmpFile.Write(plain); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	// Open $EDITOR (default: vi). $EDITOR commonly carries flags
	// ("code --wait", "vim -u NONE", "emacsclient -c"); split on whitespace so
	// they aren't treated as part of the executable path (which fails outright).
	argv := editorArgv(os.Getenv("EDITOR"), tmpPath)
	editorCmd := exec.CommandContext(sigCtx, argv[0], argv[1:]...) //nolint:gosec
	// Forward SIGTERM rather than exec's default SIGKILL, so an editor that put
	// the terminal into raw mode gets the chance to restore it — and TERM rather
	// than INT on purpose: the full-screen editors this is most likely running
	// (vi, vim, nano) all treat SIGINT as an in-editor key and keep running,
	// while SIGTERM is their "deadly signal" path, on which each restores the
	// terminal and exits at once (measured under a pty). WaitDelay escalates
	// to a kill for one that ignores even that, so the command can never wedge
	// waiting for it. Where the signal cannot be sent at all (Windows has no
	// SIGTERM delivery), exec reports the Cancel error through Wait, the
	// interrupt check below wins over it, and the outcome degrades to the
	// grace-period kill rather than to a hang.
	editorCmd.Cancel = func() error { return editorCmd.Process.Signal(syscall.SIGTERM) }
	editorCmd.WaitDelay = secretEditGrace
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr
	runErr := editorCmd.Run()
	if sigCtx.Err() != nil {
		// Interrupted: abandon the edit. Nothing is re-encrypted, and the
		// deferred os.Remove takes the cleartext temp file with it.
		return errSecretEditInterrupted
	}
	if runErr != nil {
		return fmt.Errorf("editor: %w", runErr)
	}

	// Read back edited bytes and validate with the SAME contract apply uses
	// (valid TOML + flatten: string-only leaves, no dup/colliding keys), so a
	// typo — or a `"a.b"` quoted key colliding with a `[a] b` table, or a
	// non-string value — is rejected here instead of being happily encrypted and
	// then failing every apply.
	edited, err := os.ReadFile(tmpPath)
	if err != nil {
		return fmt.Errorf("read edited tmp: %w", err)
	}
	if err := secrets.ValidateVaultTOML(edited); err != nil {
		return fmt.Errorf("edited secrets are invalid (not saved): %w", err)
	}
	// A signal that lands between the editor exiting and the vault being
	// rewritten still means "abandon": the old goroutine would have os.Exit'd
	// here — mid-WriteVerified if it was unlucky — so checking once more before
	// the write is strictly safer than what it replaces, and it is the last
	// point at which abandoning costs nothing.
	if sigCtx.Err() != nil {
		return errSecretEditInterrupted
	}
	if err := vault.WriteVerified(edited); err != nil {
		return fmt.Errorf("re-encrypt: %w", err)
	}

	success(cmd, ui.EmojiSuccess, "secrets saved")
	return nil
}

func secretsGet(cmd *cobra.Command, args []string) error {
	cfg, home, err := loadSecretsConfig()
	if err != nil {
		return err
	}
	if err := secrets.RequireAgeVault(cfg, "secret get", secrets.VaultRead); err != nil {
		return err
	}

	m, err := vaultFor(cfg, home).Load()
	if err != nil {
		return err
	}
	v, ok := secrets.GetNestedKey(m, args[0])
	if !ok {
		return fmt.Errorf("secret %q not found", args[0])
	}
	s, isStr := v.(string)
	if !isStr {
		return fmt.Errorf("secret %q is not a string value (%T); secret values must be quoted strings (apply rejects non-string secrets)", args[0], v)
	}
	// Hygiene (#200 F11): `set` prompts with echo off and steers scripts to
	// --stdin, while `get` has always printed the cleartext with no counterpart
	// caution. The value still goes to stdout UNDECORATED — `$(agentsync secret
	// get k)` must keep working — but when stdout is a terminal the value is
	// about to land in scrollback (and, pasted, in shell history), so say so on
	// stderr. Piped and redirected uses stay silent.
	fmt.Fprintln(cmd.OutOrStdout(), s)
	if stdoutIsTerminal(cmd) {
		diag(cmd, ui.LevelWarn,
			"that value is now in your terminal scrollback; pipe it (`agentsync secret get %s | …`) to keep it out.",
			ui.Sanitize(args[0]))
	}
	return nil
}

// stdoutIsTerminal reports whether the command's stdout is an interactive
// terminal — the signal that a printed secret has just entered scrollback. The
// test harness's buffer is not a terminal, so tests stay quiet.
func stdoutIsTerminal(cmd *cobra.Command) bool {
	f, ok := cmd.OutOrStdout().(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func secretsSet(cmd *cobra.Command, arg string, useStdin, allowEmpty bool) error {
	cfg, home, err := loadSecretsConfig()
	if err != nil {
		return err
	}
	if err := secrets.RequireAgeVault(cfg, "secret set", secrets.VaultWrite); err != nil {
		return err
	}

	key, value, err := resolveSecretKeyValue(cmd, arg, useStdin)
	if err != nil {
		return err
	}
	// Refuse a whitespace-only (or empty) value by default: an empty secret is
	// almost never intentional (a fat-fingered paste, an empty pbpaste pipe), is
	// invisible to the fail-closed cleartext backstop, and resolves to "" in native
	// config at apply time. The error names only the key, never the attempted value
	// (which could be a mistyped real secret). --allow-empty is the escape hatch.
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return fmt.Errorf("refusing to store an empty value for secret %q; pass --allow-empty to store it deliberately", key)
	}

	m, err := vaultFor(cfg, home).Load()
	if err != nil {
		return err
	}
	// Refuse destructive type changes BEFORE any encryption, so a refused set
	// never reaches Vault.Save/WriteVerified and the on-disk vault is left
	// byte-for-byte unchanged.
	if err := secrets.SetNestedKey(m, key, value); err != nil {
		return err
	}
	if err := vaultFor(cfg, home).Save(m); err != nil {
		return err
	}
	success(cmd, ui.EmojiSuccess, "secret %q set", key)
	return nil
}

// resolveSecretKeyValue extracts (key, value) from the user's invocation.
// Error messages here are deliberately devoid of the raw argument bytes —
// a user who mistyped `secrets set ghp_live_token` instead of
// `secrets set github.token=…` was previously greeted with
// `got "ghp_live_token"` in stderr, dumping the live secret into log
// scrollback. Now we report shape, length, and remediation only.
func resolveSecretKeyValue(cmd *cobra.Command, arg string, useStdin bool) (string, string, error) {
	if useStdin {
		// arg is the key; value comes from stdin (trim a single trailing
		// newline, but keep all other whitespace).
		key := strings.TrimSpace(arg)
		if key == "" || strings.ContainsAny(key, "= \t") {
			return "", "", fmt.Errorf("--stdin requires a single key argument (no '=' or whitespace)")
		}
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", "", fmt.Errorf("read secret from stdin: %w", err)
		}
		// Strip a SINGLE trailing newline (the shell/echo/heredoc artifact),
		// not all of them, so a secret that legitimately ends in newline(s)
		// (e.g. a PEM block) isn't silently truncated.
		v := strings.TrimSuffix(string(raw), "\n")
		v = strings.TrimSuffix(v, "\r")
		return key, v, nil
	}

	// Legacy key=value form. Warn that the value just hit argv (and
	// therefore ps(1)/history/auditd) but accept it for back-compat.
	if idx := strings.IndexByte(arg, '='); idx >= 0 {
		key := arg[:idx]
		if key == "" {
			return "", "", fmt.Errorf("set argument has empty key (expected <key>=<value>)")
		}
		value := arg[idx+1:]
		ew := cmd.ErrOrStderr()
		ep := printerOn(cmd, ew)
		ep.Fdiagf(ew, ui.LevelWarn, "passing the value on argv exposes it to ps(1), shell history, and process auditing.")
		ep.Fdetailf(ew, "Use `agentsync secret set <key> --stdin` or omit the value to be prompted.")
		return key, value, nil
	}

	// No '=' and no --stdin → prompt or error if stdin is not a TTY.
	stdin, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(stdin.Fd())) {
		// Refuse to hang silently on a non-interactive stdin. The
		// remediation pointer must NOT include the user's arg — it
		// may itself be a leaked secret.
		return "", "", fmt.Errorf("no value provided for key (argument has no '=', --stdin not set, stdin is not a terminal); use --stdin or run interactively")
	}
	key := strings.TrimSpace(arg)
	if key == "" {
		return "", "", fmt.Errorf("empty key argument")
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Enter value for %s (input hidden): ", key)
	pwBytes, err := term.ReadPassword(int(stdin.Fd()))
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return "", "", fmt.Errorf("read secret: %w", err)
	}
	return key, string(pwBytes), nil
}

// newSecretsListCmd lists the KEYS in the vault — never the values (#200 F4).
// The vault was write-and-read-one-at-a-time: there was no way to answer "what
// is in here?" short of `secret edit`, which decrypts the whole thing to a temp
// file in $EDITOR. Listing keys needs none of that exposure.
func newSecretsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "list secret KEYS in the vault (never values)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, home, err := loadSecretsConfig()
			if err != nil {
				return err
			}
			if err := secrets.RequireAgeVault(cfg, "secret list", secrets.VaultRead); err != nil {
				return err
			}
			m, err := vaultFor(cfg, home).Load()
			if err != nil {
				return err
			}
			keys := secrets.FlattenKeys(m)
			sort.Strings(keys)
			w := cmd.OutOrStdout()
			if len(keys) == 0 {
				fmt.Fprintln(w, "(vault is empty; add one with `agentsync secret set <key>`)")
				return nil
			}
			for _, k := range keys {
				fmt.Fprintln(w, ui.Sanitize(k))
			}
			return nil
		},
	}
}

// newSecretsRemoveCmd deletes one key from the vault and re-encrypts (#200 F4).
// Before this the only way to remove a secret was `secret edit` — decrypting the
// whole vault into $EDITOR to delete one line.
func newSecretsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <key>",
		Aliases: []string{"rm"},
		Short:   "delete a secret key from the vault",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error { return secretsRemove(cmd, args[0]) })
		},
	}
}

// secretsRemove deletes key from the vault and re-encrypts. It mirrors
// secretsSet's ordering exactly: mutate the decrypted map FIRST and only
// encrypt on success, so a refused removal (missing key) leaves the on-disk
// vault byte-for-byte unchanged.
func secretsRemove(cmd *cobra.Command, key string) error {
	cfg, home, err := loadSecretsConfig()
	if err != nil {
		return err
	}
	if err := secrets.RequireAgeVault(cfg, "secret remove", secrets.VaultWrite); err != nil {
		return err
	}
	m, err := vaultFor(cfg, home).Load()
	if err != nil {
		return err
	}
	if _, ok := secrets.GetNestedKey(m, key); !ok {
		return fmt.Errorf("secret %q not found; run `agentsync secret list` to see the keys in the vault", key)
	}
	if !secrets.DeleteNestedKey(m, key) {
		return fmt.Errorf("secret %q could not be removed (it is a table, not a value); edit the vault with `agentsync secret edit`", key)
	}
	if err := vaultFor(cfg, home).Save(m); err != nil {
		return err
	}
	success(cmd, ui.EmojiRemoved, "secret %q removed", key)
	return nil
}
