package secrets

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"github.com/pelletier/go-toml/v2"
)

// AgeBackend reads an age-encrypted TOML file (secrets.age), decrypts it using
// the identity file specified in opensync.toml [secrets], parses as TOML, and
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
	b.cache = flatten("", top)
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
func flatten(prefix string, m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch vv := v.(type) {
		case map[string]any:
			for kk, vvv := range flatten(key, vv) {
				out[kk] = vvv
			}
		case string:
			out[key] = vv
		default:
			out[key] = fmt.Sprint(vv)
		}
	}
	return out
}

// Encrypt writes plaintext as-is, encrypted to the given age X25519 recipient
// public key, into dest. The file is created or truncated (mode 0600).
// plaintext is typically TOML-formatted secrets.
func Encrypt(plaintext []byte, recipient string, dest string) error {
	rec, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		return fmt.Errorf("parse age recipient: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", dest, err)
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := age.Encrypt(f, rec)
	if err != nil {
		return fmt.Errorf("init age encrypt: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return err
	}
	return w.Close()
}

// Decrypt decrypts an age-encrypted file using the identity in identityFile
// and returns the plaintext bytes.
func Decrypt(ageFile, identityFile string) ([]byte, error) {
	b := NewAgeBackend(ageFile, identityFile)
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

