package secrets_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"
	secrets_pkg "github.com/spxrogers/agentsync/internal/secrets"
)

func TestAgeRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	// Generate a fresh age identity.
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	idPath := filepath.Join(tmp, "id.txt")
	if err := os.WriteFile(idPath, []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := id.Recipient().String()

	plain := []byte(`[github]
token = "ghp_abc"
[linear]
api_key = "lin_xyz"
`)
	agePath := filepath.Join(tmp, "secrets.age")
	if err := secrets_pkg.Encrypt(plain, rec, agePath); err != nil {
		t.Fatal(err)
	}

	b := secrets_pkg.NewAgeBackend(agePath, idPath)
	if v, err := b.Resolve("github.token"); err != nil || v != "ghp_abc" {
		t.Fatalf("github.token = %q (err: %v)", v, err)
	}
	if v, err := b.Resolve("linear.api_key"); err != nil || v != "lin_xyz" {
		t.Fatalf("linear.api_key = %q (err: %v)", v, err)
	}
}

// TestAgeBackend_NonStringValueRejected is the regression for the bug where
// flatten coerced non-string TOML leaves via fmt.Sprint: a numeric secret
// like `token = 0123` silently resolved to "123" instead of erroring, so a
// mistyped (unquoted) credential landed wrong in the agent config with no
// diagnostic. Now load() fails loudly.
func TestAgeBackend_NonStringValueRejected(t *testing.T) {
	tmp := t.TempDir()
	id, _ := age.GenerateX25519Identity()
	idPath := filepath.Join(tmp, "id.txt")
	if err := os.WriteFile(idPath, []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	agePath := filepath.Join(tmp, "secrets.age")
	// token is an unquoted integer — a common mistake.
	if err := secrets_pkg.Encrypt([]byte("[github]\ntoken = 1234\n"), id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}

	b := secrets_pkg.NewAgeBackend(agePath, idPath)
	_, err := b.Resolve("github.token")
	if err == nil {
		t.Fatal("expected error resolving a non-string secret value; got nil")
	}
	if !strings.Contains(err.Error(), "non-string") {
		t.Fatalf("error should explain the non-string value; got: %v", err)
	}
}

func TestAgeBackend_MissingKey(t *testing.T) {
	tmp := t.TempDir()
	id, _ := age.GenerateX25519Identity()
	idPath := filepath.Join(tmp, "id.txt")
	_ = os.WriteFile(idPath, []byte(id.String()), 0o600)
	agePath := filepath.Join(tmp, "secrets.age")
	_ = secrets_pkg.Encrypt([]byte("[foo]\nbar = \"baz\"\n"), id.Recipient().String(), agePath)

	b := secrets_pkg.NewAgeBackend(agePath, idPath)
	_, err := b.Resolve("missing.key")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestAgeBackend_WrongIdentity(t *testing.T) {
	tmp := t.TempDir()
	id1, _ := age.GenerateX25519Identity()
	id2, _ := age.GenerateX25519Identity()
	idPath2 := filepath.Join(tmp, "id2.txt")
	_ = os.WriteFile(idPath2, []byte(id2.String()), 0o600)
	agePath := filepath.Join(tmp, "secrets.age")
	_ = secrets_pkg.Encrypt([]byte("[foo]\nbar = \"baz\"\n"), id1.Recipient().String(), agePath)

	b := secrets_pkg.NewAgeBackend(agePath, idPath2)
	_, err := b.Resolve("foo.bar")
	if err == nil {
		t.Fatal("expected error with wrong identity")
	}
}

func TestAgeBackend_RejectsLooseIdentityPermissions(t *testing.T) {
	tmp := t.TempDir()
	id, _ := age.GenerateX25519Identity()
	idPath := filepath.Join(tmp, "id.txt")
	// 0o644 — group/other readable; this defeats the threat model.
	if err := os.WriteFile(idPath, []byte(id.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	agePath := filepath.Join(tmp, "secrets.age")
	_ = secrets_pkg.Encrypt([]byte("[foo]\nbar = \"baz\"\n"), id.Recipient().String(), agePath)

	b := secrets_pkg.NewAgeBackend(agePath, idPath)
	_, err := b.Resolve("foo.bar")
	if err == nil {
		t.Fatal("expected error for loose identity permissions")
	}
	if got := err.Error(); !contains(got, "insecure permissions") {
		t.Fatalf("error %q did not mention insecure permissions", got)
	}
}

func TestAgeBackend_SkipPermCheckOptOut(t *testing.T) {
	tmp := t.TempDir()
	id, _ := age.GenerateX25519Identity()
	idPath := filepath.Join(tmp, "id.txt")
	_ = os.WriteFile(idPath, []byte(id.String()), 0o644)
	agePath := filepath.Join(tmp, "secrets.age")
	_ = secrets_pkg.Encrypt([]byte("[foo]\nbar = \"baz\"\n"), id.Recipient().String(), agePath)

	t.Setenv(secrets_pkg.SkipPermCheckEnv, "1")
	b := secrets_pkg.NewAgeBackend(agePath, idPath)
	if v, err := b.Resolve("foo.bar"); err != nil || v != "baz" {
		t.Fatalf("with skip-env set, want baz; got %q err=%v", v, err)
	}
}

// contains is a simple substring helper to keep the assertion readable
// without pulling in strings just for one Contains call.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestDecrypt_RawBytes(t *testing.T) {
	tmp := t.TempDir()
	id, _ := age.GenerateX25519Identity()
	idPath := filepath.Join(tmp, "id.txt")
	_ = os.WriteFile(idPath, []byte(id.String()), 0o600)
	agePath := filepath.Join(tmp, "secrets.age")
	plain := []byte("hello world")
	_ = secrets_pkg.Encrypt(plain, id.Recipient().String(), agePath)

	got, err := secrets_pkg.Decrypt(agePath, idPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Fatalf("expected %q, got %q", plain, got)
	}
}

// TestBackendSetButEmpty pins that both backends use PRESENCE (not emptiness)
// semantics (issue #163): a present-but-empty value resolves to ("", nil); only a
// genuinely-absent key errors. AgeBackend already did this; EnvBackend now matches
// it (os.LookupEnv instead of os.Getenv=="").
func TestBackendSetButEmpty(t *testing.T) {
	t.Run("env/set-empty", func(t *testing.T) {
		t.Setenv("AGENTSYNC_EMPTY_TEST_VAR", "")
		v, err := secrets_pkg.EnvBackend{}.Resolve("AGENTSYNC_EMPTY_TEST_VAR")
		if err != nil || v != "" {
			t.Fatalf("set-but-empty env var: got (%q, %v), want (\"\", nil)", v, err)
		}
	})
	t.Run("env/unset", func(t *testing.T) {
		if _, err := (secrets_pkg.EnvBackend{}).Resolve("AGENTSYNC_UNSET_TEST_VAR_XYZ"); err == nil {
			t.Fatal("unset env var should error")
		}
	})
	// AgeBackend cases build from a real on-disk age vault fixture (not a
	// hand-populated cache), per the artifact-fidelity rule.
	newVault := func(t *testing.T, body string) *secrets_pkg.AgeBackend {
		t.Helper()
		tmp := t.TempDir()
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		idPath := filepath.Join(tmp, "id.txt")
		if err := os.WriteFile(idPath, []byte(id.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		agePath := filepath.Join(tmp, "secrets.age")
		if err := secrets_pkg.Encrypt([]byte(body), id.Recipient().String(), agePath); err != nil {
			t.Fatal(err)
		}
		return secrets_pkg.NewAgeBackend(agePath, idPath)
	}
	t.Run("age/set-empty", func(t *testing.T) {
		b := newVault(t, "[github]\ntoken = \"\"\n") // empty-string leaf — present, not missing
		v, err := b.Resolve("github.token")
		if err != nil || v != "" {
			t.Fatalf("set-but-empty age secret: got (%q, %v), want (\"\", nil)", v, err)
		}
	})
	t.Run("age/missing", func(t *testing.T) {
		b := newVault(t, "[github]\ntoken = \"x\"\n")
		if _, err := b.Resolve("github.absent"); err == nil {
			t.Fatal("missing age key should error")
		}
	})
}

// TestCheckIdentityPermissions is the direct unit test P5 asks for: a PRESENT but
// group/other-readable identity file is still rejected (the stat-error bypass only
// covers absent/unreadable files), while a 0600 file passes. (issue #163)
func TestCheckIdentityPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits don't reflect Windows ACLs; the check no-ops there")
	}
	t.Setenv(secrets_pkg.SkipPermCheckEnv, "") // ensure the gate is NOT bypassed
	tmp := t.TempDir()

	loose := filepath.Join(tmp, "loose.key")
	if err := os.WriteFile(loose, []byte("id"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := secrets_pkg.CheckIdentityPermissions(loose); err == nil {
		t.Fatal("0644 identity (group/other readable) must be rejected")
	} else if !contains(err.Error(), "insecure permissions") {
		t.Fatalf("error should mention insecure permissions; got: %v", err)
	}

	tight := filepath.Join(tmp, "tight.key")
	if err := os.WriteFile(tight, []byte("id"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secrets_pkg.CheckIdentityPermissions(tight); err != nil {
		t.Fatalf("0600 identity must pass; got: %v", err)
	}

	// Absent file is bypassed (the caller surfaces the real open error), not
	// reported as a permission error.
	if err := secrets_pkg.CheckIdentityPermissions(filepath.Join(tmp, "nope.key")); err != nil {
		t.Fatalf("absent identity must be bypassed (nil), not a fake perm error; got: %v", err)
	}
}
