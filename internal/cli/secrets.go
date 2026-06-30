package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"golang.org/x/term"
)

func newSecretsCmd() *cobra.Command {
	sec := &cobra.Command{Use: "secrets", Short: "manage age-encrypted secrets"}
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
	)
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

// resolveAgePath returns the absolute path to the secrets.age file, applying
// the same default + ${env:HOME}/~ expansion the apply path uses.
func resolveAgePath(cfg source.SecretsConfig, home string) string {
	return secrets.ResolveAgeFile(cfg, home, paths.HomeDir(paths.OSEnv{}))
}

// resolveIdentityPath returns the absolute identity_file path, expanding
// ${env:HOME}/~ the same way the apply path does — without this, `secrets
// get/set/edit` would os.ReadFile a literal "${env:HOME}/..." string and
// fail even though the documented init template uses exactly that form.
func resolveIdentityPath(cfg source.SecretsConfig, home string) string {
	return secrets.ResolveIdentityFile(cfg, home, paths.HomeDir(paths.OSEnv{}))
}

// decryptToMap decrypts secrets.age and returns the top-level map.
// If the file does not exist, returns an empty map.
func decryptToMap(cfg source.SecretsConfig, home string) (map[string]any, error) {
	agePath := resolveAgePath(cfg, home)
	if _, err := os.Stat(agePath); os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	plain, err := secrets.Decrypt(agePath, resolveIdentityPath(cfg, home))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := toml.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("parse secrets TOML: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// encryptMap marshals m as TOML and encrypts to secrets.age, verifying the
// result is decryptable by the configured identity (see writeSecretsVerified).
func encryptMap(m map[string]any, cfg source.SecretsConfig, home string) error {
	plain, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal secrets TOML: %w", err)
	}
	return writeSecretsVerified(plain, cfg, home)
}

// writeSecretsVerified encrypts plain to the age store and then verifies the
// configured identity_file can actually decrypt the result, rolling back the
// previous store on failure. Without this, a recipient that does not match the
// identity (a typo, or a key swap) silently re-encrypts the whole store to a
// key the user cannot read — locking them out of their own secrets with a
// cheerful "set" message.
func writeSecretsVerified(plain []byte, cfg source.SecretsConfig, home string) error {
	agePath := resolveAgePath(cfg, home)
	prev, hadPrev := []byte(nil), false
	if data, err := os.ReadFile(agePath); err == nil {
		prev, hadPrev = data, true
	}
	if err := secrets.Encrypt(plain, cfg.Recipient, agePath); err != nil {
		return err
	}
	if _, err := secrets.Decrypt(agePath, resolveIdentityPath(cfg, home)); err != nil {
		// Roll back so a misconfiguration can never destroy access to the store.
		if hadPrev {
			_ = os.WriteFile(agePath, prev, 0o600)
		} else {
			_ = os.Remove(agePath)
		}
		return fmt.Errorf("the new secrets would be unreadable by identity_file (recipient %q does not match it); "+
			"refusing to lock you out — check [secrets].recipient and identity_file: %w", cfg.Recipient, err)
	}
	return nil
}

// setNestedKey sets a dotted key in a nested map, creating intermediate maps
// as needed.
func setNestedKey(m map[string]any, dottedKey, value string) {
	parts := strings.SplitN(dottedKey, ".", 2)
	if len(parts) == 1 {
		m[parts[0]] = value
		return
	}
	sub, ok := m[parts[0]]
	if !ok {
		sub = map[string]any{}
		m[parts[0]] = sub
	}
	subMap, ok := sub.(map[string]any)
	if !ok {
		// overwrite non-map with map
		subMap = map[string]any{}
		m[parts[0]] = subMap
	}
	setNestedKey(subMap, parts[1], value)
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

// getNestedKey retrieves a dotted key from a nested map, returning the raw
// value so the caller can enforce the string-only contract (apply rejects
// non-string leaves; `get` must not print a value apply would refuse).
func getNestedKey(m map[string]any, dottedKey string) (any, bool) {
	parts := strings.SplitN(dottedKey, ".", 2)
	v, ok := m[parts[0]]
	if !ok {
		return nil, false
	}
	if len(parts) == 1 {
		return v, true
	}
	sub, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	return getNestedKey(sub, parts[1])
}

func secretsEdit(cmd *cobra.Command, _ []string) error {
	cfg, home, err := loadSecretsConfig()
	if err != nil {
		return err
	}
	if cfg.Backend != "age" {
		return fmt.Errorf("secrets edit requires backend = \"age\" in agentsync.toml [secrets]")
	}
	if cfg.Recipient == "" {
		return fmt.Errorf("secrets edit requires [secrets].recipient in agentsync.toml")
	}
	if cfg.IdentityFile == "" {
		return fmt.Errorf("secrets edit requires [secrets].identity_file in agentsync.toml")
	}

	agePath := resolveAgePath(cfg, home)
	var plain []byte
	if _, err := os.Stat(agePath); os.IsNotExist(err) {
		plain = []byte("# agentsync secrets — TOML format\n# Example:\n# [github]\n# token = \"ghp_...\"\n")
	} else {
		plain, err = secrets.Decrypt(agePath, resolveIdentityPath(cfg, home))
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
		_ = os.Remove(tmpPath)
	}()
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
			_ = os.Remove(tmpPath)
			os.Exit(130) // 128 + SIGINT
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
	if err := writeSecretsVerified(edited, cfg, home); err != nil {
		return fmt.Errorf("re-encrypt: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "secrets saved")
	return nil
}

func secretsGet(cmd *cobra.Command, args []string) error {
	cfg, home, err := loadSecretsConfig()
	if err != nil {
		return err
	}
	if cfg.Backend != "age" {
		return fmt.Errorf("secrets get requires backend = \"age\" in agentsync.toml [secrets]")
	}

	m, err := decryptToMap(cfg, home)
	if err != nil {
		return err
	}
	v, ok := getNestedKey(m, args[0])
	if !ok {
		return fmt.Errorf("secret %q not found", args[0])
	}
	s, isStr := v.(string)
	if !isStr {
		return fmt.Errorf("secret %q is not a string value (%T); secret values must be quoted strings (apply rejects non-string secrets)", args[0], v)
	}
	fmt.Fprintln(cmd.OutOrStdout(), s)
	return nil
}

func secretsSet(cmd *cobra.Command, arg string, useStdin, allowEmpty bool) error {
	cfg, home, err := loadSecretsConfig()
	if err != nil {
		return err
	}
	if cfg.Backend != "age" {
		return fmt.Errorf("secrets set requires backend = \"age\" in agentsync.toml [secrets]")
	}
	if cfg.Recipient == "" {
		return fmt.Errorf("secrets set requires [secrets].recipient in agentsync.toml")
	}
	if cfg.IdentityFile == "" {
		return fmt.Errorf("secrets set requires [secrets].identity_file in agentsync.toml")
	}

	key, value, err := resolveSecretKeyValue(cmd, arg, useStdin)
	if err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" && !allowEmpty {
		return fmt.Errorf("refusing to store an empty or whitespace-only value for secret %q; pass --allow-empty to store it deliberately", key)
	}

	m, err := decryptToMap(cfg, home)
	if err != nil {
		return err
	}
	setNestedKey(m, key, value)
	if err := encryptMap(m, cfg, home); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "secret %q set\n", key)
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
		fmt.Fprintln(cmd.ErrOrStderr(),
			"agentsync: warning: passing the value on argv exposes it to ps(1), shell history, and process auditing.")
		fmt.Fprintln(cmd.ErrOrStderr(),
			"  Use `agentsync secrets set <key> --stdin` or omit the value to be prompted.")
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
