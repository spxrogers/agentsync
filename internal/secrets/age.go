package secrets

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"github.com/pelletier/go-toml/v2"
	"github.com/spxrogers/agentsync/internal/iox"
)

// AgeBackend reads an age-encrypted TOML file (secrets.age), decrypts it using
// the identity file specified in agentsync.toml [secrets], parses as TOML, and
// resolves dotted keys. Cleartext is held in memory only and never written to
// durable storage.
type AgeBackend struct {
	AgeFile      string // path to secrets.age
	IdentityFile string // path to age identity (private key)
	cache        map[string]string
}

// NewAgeBackend returns an AgeBackend configured with the given file paths.
func NewAgeBackend(ageFile, identityFile string) *AgeBackend {
	return &AgeBackend{AgeFile: ageFile, IdentityFile: identityFile}
}

// load decrypts and parses the age file, populating the in-memory cache.
// Subsequent calls are no-ops (lazy + cached).
func (b *AgeBackend) load() error {
	if b.cache != nil {
		return nil
	}
	if err := CheckIdentityPermissions(b.IdentityFile); err != nil {
		return err
	}
	idData, err := os.ReadFile(b.IdentityFile)
	if err != nil {
		return fmt.Errorf("read identity %s: %w", b.IdentityFile, err)
	}
	ids, err := age.ParseIdentities(strings.NewReader(string(idData)))
	if err != nil {
		return fmt.Errorf("parse age identity: %w", err)
	}
	encFile, err := os.Open(b.AgeFile)
	if err != nil {
		return fmt.Errorf("open age file %s: %w", b.AgeFile, err)
	}
	defer encFile.Close()
	rd, err := age.Decrypt(encFile, ids...)
	if err != nil {
		return fmt.Errorf("decrypt %s: %w", b.AgeFile, err)
	}
	raw, err := io.ReadAll(rd)
	if err != nil {
		return fmt.Errorf("read decrypted: %w", err)
	}
	var top map[string]any
	if err := toml.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("parse decrypted as TOML: %w", err)
	}
	cache, err := flatten("", top)
	if err != nil {
		return fmt.Errorf("parse decrypted secrets: %w", err)
	}
	b.cache = cache
	return nil
}

// Resolve returns the cleartext value for a dotted key like "github.token".
func (b *AgeBackend) Resolve(dottedKey string) (string, error) {
	if err := b.load(); err != nil {
		return "", err
	}
	v, ok := b.cache[dottedKey]
	if !ok {
		return "", fmt.Errorf("secret %q not found", dottedKey)
	}
	return v, nil
}

// flatten recursively converts a nested map[string]any into a flat
// map[string]string with dotted keys, e.g. {"github": {"token": "x"}} ->
// {"github.token": "x"}.
//
// Non-string leaf values (numbers, bools, arrays, datetimes) are rejected
// rather than coerced via fmt.Sprint: a TOML `token = 0123` would otherwise
// resolve to "123" (or "83" for octal) and an array to Go's "[a b]" syntax,
// substituting a silently-wrong credential into the user's agent config.
// ValidateVaultTOML parses decrypted vault bytes as TOML and applies the exact
// contract apply uses at resolve time (flatten: string-only leaves, no
// dup/colliding keys). `secrets edit` calls it so a vault that apply would later
// refuse is rejected at save time rather than silently encrypted.
func ValidateVaultTOML(data []byte) error {
	var m map[string]any
	if err := toml.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("not valid TOML: %w", err)
	}
	_, err := flatten("", m)
	return err
}

func flatten(prefix string, m map[string]any) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch vv := v.(type) {
		case map[string]any:
			sub, err := flatten(key, vv)
			if err != nil {
				return nil, err
			}
			for kk, vvv := range sub {
				if _, dup := out[kk]; dup {
					return nil, fmt.Errorf("secret key %q is defined more than once (e.g. both a quoted %q key and a nested table); use exactly one form", kk, kk)
				}
				out[kk] = vvv
			}
		case string:
			if _, dup := out[key]; dup {
				return nil, fmt.Errorf("secret key %q is defined more than once (e.g. both a quoted %q key and a nested table); use exactly one form", key, key)
			}
			out[key] = vv
		default:
			return nil, fmt.Errorf("secret %q has a non-string value (%T); secret values must be quoted strings", key, vv)
		}
	}
	return out, nil
}

// Encrypt writes plaintext as-is, encrypted to the given age X25519
// recipient public key, into dest. The encrypted bytes are produced in
// memory and committed via the iox atomic-write path: write to a sibling
// tmp file (0o600), fsync, rename onto dest. This is critical for
// secrets.age — a previous implementation opened dest with O_TRUNC and
// streamed encrypted bytes into it; a Ctrl-C, panic, or disk-full
// between truncate and close left a zero-byte secrets.age with NO
// backup, losing every key the user had ever stored.
//
// plaintext is typically TOML-formatted secrets.
func Encrypt(plaintext []byte, recipient string, dest string) error {
	rec, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		return fmt.Errorf("parse age recipient: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", dest, err)
	}
	// Encrypt to an in-memory buffer first so a write failure mid-stream
	// cannot truncate the user's existing secrets.age.
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rec)
	if err != nil {
		return fmt.Errorf("init age encrypt: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return fmt.Errorf("write age stream: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close age stream: %w", err)
	}
	return iox.AtomicWrite(dest, buf.Bytes(), 0o600)
}

// Decrypt decrypts an age-encrypted file using the identity in identityFile
// and returns the plaintext bytes.
func Decrypt(ageFile, identityFile string) ([]byte, error) {
	b := NewAgeBackend(ageFile, identityFile)
	if err := CheckIdentityPermissions(identityFile); err != nil {
		return nil, err
	}
	// We want raw bytes, not parsed; bypass the cache and re-decrypt directly.
	idData, err := os.ReadFile(identityFile)
	if err != nil {
		return nil, fmt.Errorf("read identity %s: %w", identityFile, err)
	}
	ids, err := age.ParseIdentities(strings.NewReader(string(idData)))
	if err != nil {
		return nil, fmt.Errorf("parse age identity: %w", err)
	}
	encFile, err := os.Open(ageFile)
	if err != nil {
		return nil, fmt.Errorf("open age file %s: %w", ageFile, err)
	}
	defer encFile.Close()
	rd, err := age.Decrypt(encFile, ids...)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", ageFile, err)
	}
	raw, err := io.ReadAll(rd)
	_ = b // suppress unused warning
	return raw, err
}

// SkipPermCheckEnv is the env var that disables CheckIdentityPermissions.
// Set to "1" for setups (network homes, dev containers) where mode bits
// don't reflect actual access (e.g. NFS roots squash, ACLs override).
const SkipPermCheckEnv = "AGENTSYNC_AGE_SKIP_PERM_CHECK"

// CheckIdentityPermissions ensures the age identity file is not readable by
// group or other. A 0o644 identity file defeats the purpose of encryption.
// On Windows the mode bits are not POSIX semantics; we skip the check there.
// Users can opt out by setting AGENTSYNC_AGE_SKIP_PERM_CHECK=1.
func CheckIdentityPermissions(path string) error {
	if path == "" {
		return nil
	}
	if os.Getenv(SkipPermCheckEnv) == "1" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		// Surface the open error from the caller, not a fake permission error.
		return nil
	}
	mode := info.Mode().Perm()
	// On Windows, os.Stat reports synthesized POSIX bits that don't reflect
	// real ACL semantics. Don't gate on them.
	if runtimeIsWindows() {
		return nil
	}
	if mode&0o077 != 0 {
		return fmt.Errorf("age identity %s has insecure permissions %#o (group/other readable); chmod 600 it, or set %s=1 to override",
			path, mode, SkipPermCheckEnv)
	}
	return nil
}
