package secrets_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// mapBackend is a simple Resolver backed by a map, for tests.
type mapBackend map[string]string

func (m mapBackend) Resolve(key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", &notFoundError{key}
	}
	return v, nil
}

type notFoundError struct{ key string }

func (e *notFoundError) Error() string { return "key not found: " + e.key }

func TestSubstituteRefs_SecretsAndEnv(t *testing.T) {
	sec := mapBackend{"github.token": "ghp_abc"}
	env := mapBackend{"HOME": "/Users/x"}
	got, unresolved, err := secrets.SubstituteRefs(
		"token=${secret:github.token}; home=${env:HOME}; ?=${secret:missing}",
		sec, env,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "token=ghp_abc") {
		t.Fatalf("substitution failed: %s", got)
	}
	if !strings.Contains(got, "home=/Users/x") {
		t.Fatalf("env substitution failed: %s", got)
	}
	if len(unresolved) != 1 {
		t.Fatalf("expected 1 unresolved, got %v", unresolved)
	}
	if unresolved[0] != "${secret:missing}" {
		t.Fatalf("expected ${secret:missing}, got %s", unresolved[0])
	}
}

func TestSubstituteRefs_NoRefs(t *testing.T) {
	got, unresolved, err := secrets.SubstituteRefs("plain string", secrets.NopResolver{}, secrets.NopResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "plain string" {
		t.Fatalf("expected no-op, got %q", got)
	}
	if len(unresolved) != 0 {
		t.Fatalf("expected 0 unresolved, got %v", unresolved)
	}
}

func TestSubstituteRefs_EmptyString(t *testing.T) {
	got, _, _ := secrets.SubstituteRefs("", secrets.NopResolver{}, secrets.NopResolver{})
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestEnvBackend_Resolve(t *testing.T) {
	// We inject the env via the package-level var to avoid polluting test env.
	t.Setenv("TEST_SECRET_VAR", "supersecret")
	b := secrets.EnvBackend{}
	v, err := b.Resolve("TEST_SECRET_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if v != "supersecret" {
		t.Fatalf("expected supersecret, got %q", v)
	}
}

func TestEnvBackend_MissingKey(t *testing.T) {
	b := secrets.EnvBackend{}
	_, err := b.Resolve("AGENTSYNC_DEFINITELY_NOT_SET_XYZ")
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}

// TestEnvBackend_EmptyVsUnset pins the empty-vs-unset contract EnvBackend settled
// on in #163: presence (not emptiness) is the criterion. A var set to "" resolves
// to ("", nil) — matching AgeBackend's cache-presence model — while only an UNSET
// var errors. os.LookupEnv (via the package-private osLookupEnv seam) is what
// distinguishes the two.
//
// NOTE (divergence from the #167 issue text): the issue was filed against an older
// EnvBackend that treated empty and unset IDENTICALLY (both → error) via an
// osGetenv indirection, and asked this test to assert "both → error". That code no
// longer exists; asserting it would be false. The safety the issue cares about —
// an env value cannot reach the fail-closed backstop as a cleartext "" — is still
// guaranteed, but by a DIFFERENT mechanism: ${env:…} is never inverted by
// re-reference, and backstopSecretValues drops every len-0 value, so an empty
// resolution can never enter the backstop's detection set regardless of whether
// EnvBackend returns ("", nil) or an error. This test therefore asserts the ACTUAL
// (post-#163) behavior.
func TestEnvBackend_EmptyVsUnset(t *testing.T) {
	b := secrets.EnvBackend{}

	t.Run("unset returns an error", func(t *testing.T) {
		if _, err := b.Resolve("AGENTSYNC_ENVBACKEND_UNSET_XYZ"); err == nil {
			t.Fatal("an unset env var must resolve to an error")
		}
	})

	t.Run("present-but-empty resolves to empty, not an error", func(t *testing.T) {
		t.Setenv("AGENTSYNC_ENVBACKEND_EMPTY", "")
		v, err := b.Resolve("AGENTSYNC_ENVBACKEND_EMPTY")
		if err != nil {
			t.Fatalf("a present-but-empty env var must resolve to (\"\", nil), got err=%v", err)
		}
		if v != "" {
			t.Fatalf("present-but-empty resolved to %q, want \"\"", v)
		}
	})

	t.Run("present-and-set resolves to its value", func(t *testing.T) {
		t.Setenv("AGENTSYNC_ENVBACKEND_SET", "value")
		v, err := b.Resolve("AGENTSYNC_ENVBACKEND_SET")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != "value" {
			t.Fatalf("resolved %q, want \"value\"", v)
		}
	})
}

// TestSelectBackend_RelativeFilePathUsesFilepathJoin guards against the
// pre-fix bug where SelectBackend joined homeDir+"/"+ageFile with a
// hard-coded forward slash, breaking on Windows.
func TestSelectBackend_RelativeFilePathUsesFilepathJoin(t *testing.T) {
	tmp := t.TempDir()
	cfg := source.SecretsConfig{
		Backend:      "age",
		File:         filepath.Join("secrets", "secrets.age"),
		IdentityFile: filepath.Join(tmp, "key"),
	}
	r := secrets.SelectBackend(cfg, tmp, tmp)
	// The resolver is an *AgeBackend; calling Resolve forces it to read AgeFile.
	// We don't care that decryption fails (the file doesn't exist) — we care
	// that the error references a path joined with the OS separator, not "/".
	_, err := r.Resolve("anything")
	if err == nil {
		t.Fatal("expected error from missing identity file")
	}
	wantPrefix := filepath.Join(tmp, "secrets")
	if !strings.Contains(err.Error(), wantPrefix) && !strings.Contains(err.Error(), filepath.Join(tmp, "key")) {
		// Either AgeFile or IdentityFile path may surface in the error; both
		// must be filepath.Joined paths, never have a stray hard-coded "/".
		t.Fatalf("error %q did not contain a filepath.Joined path under %q", err.Error(), tmp)
	}
}
