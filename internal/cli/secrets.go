package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

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
	// The hard os.Exit(130) below stays for now: replacing it with an injectable
	// cleanup hook is a BEHAVIOUR change (the process stops exiting from a
	// goroutine) and is the only way to test it, so it is item 9b of #235 and
	// lands in that issue's behaviour-change PR, not in this boundary move.
	//
	// A plain defer does NOT run when the process dies on an unhandled signal —
	// and aborting the editor with Ctrl-C (SIGINT) is the normal way to bail on
	// an edit, which would otherwise leave the decrypted secrets on disk. Remove
	// the cleartext tmp on SIGINT/SIGTERM before exiting.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	editDone := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			_ = os.Remove(tmpPath) //nolint:forbidigo // cleartext temp file in os.TempDir(), not a native destination
			os.Exit(130)           // 128 + SIGINT
		case <-editDone:
		}
	}()
	defer func() {
		signal.Stop(sigCh)
		close(editDone)
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
	editorCmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr
	if err := editorCmd.Run(); err != nil {
		return fmt.Errorf("editor: %w", err)
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
