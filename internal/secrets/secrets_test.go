package secrets_test

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/secrets"
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
