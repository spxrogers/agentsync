package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	cmd := &cobra.Command{
		Use:   "set <key>[=<value>]",
		Short: "set (or update) a secret key (prompts securely by default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error {
				return secretsSet(cmd, args[0], useStdin)
			})
		},
	}
	cmd.Flags().BoolVar(&useStdin, "stdin", false, "read the secret value from stdin (recommended for scripts)")
	return cmd
}

// loadSecretsConfig returns the SecretsConfig and the agentsync home directory.
func loadSecretsConfig() (source.SecretsConfig, string, error) {
	home := paths.AgentsyncHome(paths.OSEnv{})
	cfgPath := filepath.Join(home, "agentsync.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return source.SecretsConfig{}, home, fmt.Errorf("read %s: %w", cfgPath, err)
	}
	var cfg source.Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return source.SecretsConfig{}, home, fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	return cfg.Secrets, home, nil
}

// resolveAgePath returns the absolute path to the secrets.age file.
func resolveAgePath(cfg source.SecretsConfig, home string) string {
	f := cfg.File
	if f == "" {
		f = "secrets/secrets.age"
	}
	if !filepath.IsAbs(f) {
		return filepath.Join(home, f)
	}
	return f
}

// decryptToMap decrypts secrets.age and returns the top-level map.
// If the file does not exist, returns an empty map.
func decryptToMap(cfg source.SecretsConfig, home string) (map[string]any, error) {
	agePath := resolveAgePath(cfg, home)
	if _, err := os.Stat(agePath); os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	plain, err := secrets.Decrypt(agePath, cfg.IdentityFile)
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

// encryptMap marshals m as TOML and encrypts to secrets.age.
func encryptMap(m map[string]any, cfg source.SecretsConfig, home string) error {
	plain, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal secrets TOML: %w", err)
	}
	agePath := resolveAgePath(cfg, home)
	return secrets.Encrypt(plain, cfg.Recipient, agePath)
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

// getNestedKey retrieves a dotted key from a nested map.
func getNestedKey(m map[string]any, dottedKey string) (string, bool) {
	parts := strings.SplitN(dottedKey, ".", 2)
	v, ok := m[parts[0]]
	if !ok {
		return "", false
	}
	if len(parts) == 1 {
		s, ok := v.(string)
		if !ok {
			return fmt.Sprint(v), true
		}
		return s, true
	}
	sub, ok := v.(map[string]any)
	if !ok {
		return "", false
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
		plain, err = secrets.Decrypt(agePath, cfg.IdentityFile)
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
	if _, err := tmpFile.Write(plain); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	// Open $EDITOR (default: vi).
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	editorCmd := exec.Command(editor, tmpPath) //nolint:gosec
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr
	if err := editorCmd.Run(); err != nil {
		return fmt.Errorf("editor: %w", err)
	}

	// Read back edited bytes and re-encrypt.
	edited, err := os.ReadFile(tmpPath)
	if err != nil {
		return fmt.Errorf("read edited tmp: %w", err)
	}
	if err := secrets.Encrypt(edited, cfg.Recipient, agePath); err != nil {
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
	fmt.Fprintln(cmd.OutOrStdout(), v)
	return nil
}

func secretsSet(cmd *cobra.Command, arg string, useStdin bool) error {
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
		v := strings.TrimRight(string(raw), "\n")
		v = strings.TrimRight(v, "\r")
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
