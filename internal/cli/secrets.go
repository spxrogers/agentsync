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
//
// Before persisting, it validates the marshaled bytes against apply's flatten
// contract (secrets.ValidateVaultTOML: string-only leaves, no dup/colliding
// keys) — the SAME guard the `secrets edit` path applies — so a `set` that would
// yield a vault apply later refuses is rejected at save time instead of being
// silently encrypted. Validation runs on the EXACT bytes that get encrypted
// (one marshal), so validated bytes == written bytes; nothing can drift between
// the check and the write.
func encryptMap(m map[string]any, cfg source.SecretsConfig, home string) error {
	plain, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal secrets TOML: %w", err)
	}
	if err := secrets.ValidateVaultTOML(plain); err != nil {
		return fmt.Errorf("resulting secrets are invalid (not saved): %w", err)
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
			_ = os.WriteFile(agePath, prev, 0o600) //nolint:forbidigo // vault rollback: restores secrets.age (canonical source), not a native destination
		} else {
			_ = os.Remove(agePath) //nolint:forbidigo // vault rollback: removes the just-written secrets.age (canonical source), not a native destination
		}
		return fmt.Errorf("the new secrets would be unreadable by identity_file (recipient %q does not match it); "+
			"refusing to lock you out — check [secrets].recipient and identity_file: %w", cfg.Recipient, err)
	}
	return nil
}

// setNestedKey sets a dotted key in a nested map, creating intermediate maps as
// needed. It refuses *destructive type changes* rather than silently destroying
// vault content — the secrets.age vault is often the only copy of a cleartext
// secret, so an overwrite here is irreversible loss:
//
//   - Nesting under an existing scalar parent (e.g. `set token.scope=…` when
//     `token` is already a scalar secret) would drop the parent's value; refused.
//   - A leaf assignment that would overwrite an existing table (e.g. `set a=…`
//     when `a` is a `[a]` table) would drop the whole sub-table; refused.
//
// The legitimate cases still succeed: an in-place scalar update (scalar leaf →
// new scalar leaf) and a new nested key under an absent or table parent. Error
// messages name the FULL dotted paths the user typed (e.g. `a.b.c` clashing
// with scalar `a.b`, not the recursion's local `b.c`/`b`) and carry only KEY
// names, never a secret *value* byte (the no-secret-in-stderr convention
// honored by resolveSecretKeyValue).
func setNestedKey(m map[string]any, dottedKey, value string) error {
	return setNestedKeyAt(m, "", dottedKey, value)
}

// setNestedKeyAt is setNestedKey's recursion. prefix is the dotted path already
// consumed by outer levels ("" at the root); joining it back onto the local key
// names is what lets a refusal deep in the recursion report the full paths the
// user typed.
func setNestedKeyAt(m map[string]any, prefix, dottedKey, value string) error {
	full := func(k string) string {
		if prefix == "" {
			return k
		}
		return prefix + "." + k
	}
	parts := strings.SplitN(dottedKey, ".", 2)
	if len(parts) == 1 {
		if existing, ok := m[parts[0]]; ok {
			if _, isMap := existing.(map[string]any); isMap {
				return fmt.Errorf("setting %q would overwrite the existing table at %q; refusing to destroy it — choose a different key or remove %q first",
					full(parts[0]), full(parts[0]), full(parts[0]))
			}
		}
		m[parts[0]] = value
		return nil
	}
	sub, ok := m[parts[0]]
	if !ok {
		sub = map[string]any{}
		m[parts[0]] = sub
	}
	subMap, ok := sub.(map[string]any)
	if !ok {
		return fmt.Errorf("setting %q would overwrite the existing secret at %q (a scalar value); refusing to destroy it — choose a different key or remove %q first",
			full(dottedKey), full(parts[0]), full(parts[0]))
	}
	return setNestedKeyAt(subMap, full(parts[0]), parts[1], value)
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
		plain = []byte("# agentsync secret vault — TOML format\n# Example:\n# [github]\n# token = \"ghp_...\"\n")
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
		_ = os.Remove(tmpPath) //nolint:forbidigo // cleartext temp file in os.TempDir(), not a native destination
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
		return fmt.Errorf("secret get requires backend = \"age\" in agentsync.toml [secrets]")
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
	// Hygiene (#200 F11): `set` prompts with echo off and steers scripts to
	// --stdin, while `get` has always printed the cleartext with no counterpart
	// caution. The value still goes to stdout UNDECORATED — `$(agentsync secret
	// get k)` must keep working — but when stdout is a terminal the value is
	// about to land in scrollback (and, pasted, in shell history), so say so on
	// stderr. Piped and redirected uses stay silent.
	fmt.Fprintln(cmd.OutOrStdout(), s)
	if stdoutIsTerminal(cmd) {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"agentsync: that value is now in your terminal scrollback; pipe it (`agentsync secret get %s | …`) to keep it out.\n",
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
	// Refuse a whitespace-only (or empty) value by default: an empty secret is
	// almost never intentional (a fat-fingered paste, an empty pbpaste pipe), is
	// invisible to the fail-closed cleartext backstop, and resolves to "" in native
	// config at apply time. The error names only the key, never the attempted value
	// (which could be a mistyped real secret). --allow-empty is the escape hatch.
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return fmt.Errorf("refusing to store an empty value for secret %q; pass --allow-empty to store it deliberately", key)
	}

	m, err := decryptToMap(cfg, home)
	if err != nil {
		return err
	}
	// Refuse destructive type changes BEFORE any encryption, so a refused set
	// never reaches encryptMap/writeSecretsVerified and the on-disk vault is left
	// byte-for-byte unchanged.
	if err := setNestedKey(m, key, value); err != nil {
		return err
	}
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
			"  Use `agentsync secret set <key> --stdin` or omit the value to be prompted.")
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
			if cfg.Backend != "age" {
				return fmt.Errorf("secret list requires backend = \"age\" in agentsync.toml [secrets]")
			}
			m, err := decryptToMap(cfg, home)
			if err != nil {
				return err
			}
			keys := flattenSecretKeys(m, "")
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

// flattenSecretKeys walks the decrypted vault and returns dotted key paths. Only
// the PATHS are collected — no value ever leaves this function.
func flattenSecretKeys(m map[string]any, prefix string) []string {
	var out []string
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if nested, ok := v.(map[string]any); ok {
			out = append(out, flattenSecretKeys(nested, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
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
	if cfg.Backend != "age" {
		return fmt.Errorf("secret remove requires backend = \"age\" in agentsync.toml [secrets]")
	}
	m, err := decryptToMap(cfg, home)
	if err != nil {
		return err
	}
	if _, ok := getNestedKey(m, key); !ok {
		return fmt.Errorf("secret %q not found; run `agentsync secret list` to see the keys in the vault", key)
	}
	if !deleteNestedKey(m, key) {
		return fmt.Errorf("secret %q could not be removed (it is a table, not a value); edit the vault with `agentsync secret edit`", key)
	}
	if err := encryptMap(m, cfg, home); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "secret %q removed\n", key)
	return nil
}

// deleteNestedKey removes a dotted key path from the decrypted vault map,
// reporting whether a VALUE was removed. A path naming an intermediate table is
// refused (ok=false): deleting a whole subtree on a typo'd key is not a thing a
// one-word command should do silently.
func deleteNestedKey(m map[string]any, key string) bool {
	parts := strings.Split(key, ".")
	cur := m
	for i, p := range parts {
		v, ok := cur[p]
		if !ok {
			return false
		}
		if i == len(parts)-1 {
			if _, isTable := v.(map[string]any); isTable {
				return false
			}
			delete(cur, p)
			return true
		}
		next, isTable := v.(map[string]any)
		if !isTable {
			return false
		}
		cur = next
	}
	return false
}
