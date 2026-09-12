package secrets

import (
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spxrogers/agentsync/internal/source"
)

// Vault is the age-encrypted secret store as a read-modify-write subject: the
// two paths it lives at, the decrypted document, and the verified save.
//
// Every one of these operations used to live in `internal/cli/secrets.go`, which
// put cleartext-vault handling — decrypt, mutate, re-encrypt, verify, roll back
// — in the cobra layer, one package away from the guards that exist to keep
// cleartext out of the canonical source. None of it ever needed a
// *cobra.Command.
//
// What a Vault deliberately does NOT do: it never touches source.Write*, never
// unwraps a Resolved, and never reaches the canonical source at all. Its file is
// secrets.age, the input side of resolution; the leak the rest of this package
// guards against is a RESOLVED value flowing the other way, into ~/.agentsync's
// TOML. A Vault value is what an AgeBackend later reads, not something written
// back through capture.
//
// A Vault is a value: the caller resolves the three inputs and hands them over,
// which is what keeps this package cobra-free and free of internal/paths.
type Vault struct {
	cfg           source.SecretsConfig
	agentsyncHome string
	userHome      string
}

// NewVault binds a vault to its configuration. agentsyncHome anchors relative
// [secrets] paths; userHome expands ${env:HOME} and a leading ~ inside them —
// pass paths.HomeDir(env), so AGENTSYNC_TARGET_ROOT is honoured exactly as the
// apply path honours it.
func NewVault(cfg source.SecretsConfig, agentsyncHome, userHome string) Vault {
	return Vault{cfg: cfg, agentsyncHome: agentsyncHome, userHome: userHome}
}

// AgeFile returns the absolute path to the secrets.age file, applying the same
// default + ${env:HOME}/~ expansion the apply path uses.
func (v Vault) AgeFile() string { return ResolveAgeFile(v.cfg, v.agentsyncHome, v.userHome) }

// IdentityFile returns the absolute identity_file path, expanding ${env:HOME}/~
// the same way the apply path does — without this, `secret get/set/edit` would
// os.ReadFile a literal "${env:HOME}/..." string and fail even though the
// documented init template uses exactly that form.
func (v Vault) IdentityFile() string { return ResolveIdentityFile(v.cfg, v.agentsyncHome, v.userHome) }

// Load decrypts secrets.age and returns the top-level map.
// If the file does not exist, returns an empty map.
func (v Vault) Load() (map[string]any, error) {
	agePath := v.AgeFile()
	if _, err := os.Stat(agePath); os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	plain, err := Decrypt(agePath, v.IdentityFile())
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

// Save marshals m as TOML and encrypts it to secrets.age, verifying the result
// is decryptable by the configured identity (see WriteVerified).
//
// Before persisting, it validates the marshaled bytes against apply's flatten
// contract (ValidateVaultTOML: string-only leaves, no dup/colliding keys) — the
// SAME guard the `secret edit` path applies — so a `set` that would yield a
// vault apply later refuses is rejected at save time instead of being silently
// encrypted. Validation runs on the EXACT bytes that get encrypted (one
// marshal), so validated bytes == written bytes; nothing can drift between the
// check and the write.
func (v Vault) Save(m map[string]any) error {
	plain, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal secrets TOML: %w", err)
	}
	if err := ValidateVaultTOML(plain); err != nil {
		return fmt.Errorf("resulting secrets are invalid (not saved): %w", err)
	}
	return v.WriteVerified(plain)
}

// WriteVerified encrypts plain to the age store and then verifies the configured
// identity_file can actually decrypt the result, rolling back the previous store
// on failure. Without this, a recipient that does not match the identity (a
// typo, or a key swap) silently re-encrypts the whole store to a key the user
// cannot read — locking them out of their own secrets with a cheerful "set"
// message.
//
// It does NOT validate plain against apply's flatten contract: Save does, on
// the bytes it marshals itself. A caller that brings its own bytes — `secret
// edit`, handing back what the editor wrote — MUST run ValidateVaultTOML first,
// as that caller does; this method's job is the encrypt-verify-roll-back step
// and nothing about the document's shape.
func (v Vault) WriteVerified(plain []byte) error {
	agePath := v.AgeFile()
	prev, hadPrev := []byte(nil), false
	// Through the shape gate, not os.ReadFile: os.ReadFile blocks on a FIFO
	// exactly as os.Open does, and `secret edit` reaches here WITHOUT having
	// decrypted whenever the vault was absent at its own stat (the
	// os.IsNotExist arm below), so nothing else refuses a non-regular vault
	// first. A non-regular path simply reports no previous vault to preserve,
	// which is the truth: a FIFO is not a vault.
	if data, err := ReadVault(agePath); err == nil {
		prev, hadPrev = data, true
	}
	if err := Encrypt(plain, v.cfg.Recipient, agePath); err != nil {
		return err
	}
	if _, err := Decrypt(agePath, v.IdentityFile()); err != nil {
		// Roll back so a misconfiguration can never destroy access to the store.
		if hadPrev {
			_ = os.WriteFile(agePath, prev, 0o600) //nolint:forbidigo // vault rollback: restores secrets.age (canonical source), not a native destination
		} else {
			_ = os.Remove(agePath) //nolint:forbidigo // vault rollback: removes the just-written secrets.age (canonical source), not a native destination
		}
		return fmt.Errorf("the new secrets would be unreadable by identity_file (recipient %q does not match it); "+
			"refusing to lock you out — check [secrets].recipient and identity_file: %w", v.cfg.Recipient, err)
	}
	return nil
}

// SetNestedKey sets a dotted key in a nested map, creating intermediate maps as
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
// honored by the CLI's resolveSecretKeyValue).
//
// m must be non-nil: a nil map cannot be assigned into, and this function has
// no way to hand a fresh one back. Vault.Load returns a writable map even for
// an empty vault, so its callers never see that panic.
func SetNestedKey(m map[string]any, dottedKey, value string) error {
	return setNestedKeyAt(m, "", dottedKey, value)
}

// setNestedKeyAt is SetNestedKey's recursion. prefix is the dotted path already
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

// GetNestedKey retrieves a dotted key from a nested map, returning the raw
// value so the caller can enforce the string-only contract (apply rejects
// non-string leaves; `get` must not print a value apply would refuse).
func GetNestedKey(m map[string]any, dottedKey string) (any, bool) {
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
	return GetNestedKey(sub, parts[1])
}

// DeleteNestedKey removes a dotted key path from the decrypted vault map,
// reporting whether a VALUE was removed. A path naming an intermediate table is
// refused (ok=false): deleting a whole subtree on a typo'd key is not a thing a
// one-word command should do silently.
func DeleteNestedKey(m map[string]any, key string) bool {
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

// FlattenKeys walks the decrypted vault and returns dotted key paths. Only
// the PATHS are collected — no value ever leaves this function.
func FlattenKeys(m map[string]any) []string {
	return flattenKeysAt(m, "")
}

// flattenKeysAt is FlattenKeys's recursion; prefix is the dotted path already
// consumed by the outer levels ("" at the root).
func flattenKeysAt(m map[string]any, prefix string) []string {
	var out []string
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if nested, ok := v.(map[string]any); ok {
			out = append(out, flattenKeysAt(nested, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}
