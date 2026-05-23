package secrets

import (
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

func TestResolveAgeFile(t *testing.T) {
	const ah = "/home/alice/.agentsync"
	const uh = "/home/alice"
	cases := []struct {
		name string
		file string
		want string
	}{
		{"empty falls back to default", "", filepath.Join(ah, "secrets/secrets.age")},
		{"relative joins under agentsync home", "secrets/x.age", filepath.Join(ah, "secrets/x.age")},
		{"absolute stays absolute", "/etc/x.age", "/etc/x.age"},
		{"env-home expands to user home", "${env:HOME}/vault/x.age", filepath.Join(uh, "vault/x.age")},
		{"tilde expands to user home", "~/vault/x.age", filepath.Join(uh, "vault/x.age")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveAgeFile(source.SecretsConfig{File: tc.file}, ah, uh)
			if got != tc.want {
				t.Fatalf("ResolveAgeFile(%q) = %q, want %q", tc.file, got, tc.want)
			}
		})
	}
}

func TestResolveIdentityFile(t *testing.T) {
	const ah = "/home/alice/.agentsync"
	const uh = "/home/alice"
	cases := []struct {
		name     string
		identity string
		want     string
	}{
		{"empty returns empty", "", ""},
		{"env-home identity (the init-template shape)", "${env:HOME}/.config/agentsync/age.key", filepath.Join(uh, ".config/agentsync/age.key")},
		{"absolute stays absolute", "/keys/age.key", "/keys/age.key"},
		{"relative joins under agentsync home", "keys/age.key", filepath.Join(ah, "keys/age.key")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveIdentityFile(source.SecretsConfig{IdentityFile: tc.identity}, ah, uh)
			if got != tc.want {
				t.Fatalf("ResolveIdentityFile(%q) = %q, want %q", tc.identity, got, tc.want)
			}
		})
	}
}

// TestResolveAgeFile_EnvHomeHonoursRedirect proves ${env:HOME} expands to the
// passed userHome (which production wires to paths.HomeDir, honouring
// AGENTSYNC_TARGET_ROOT) rather than the raw $HOME env var — the previous
// expandEnvHome used os.Getenv("HOME") directly and so could not be
// redirected under test or a relocated home.
func TestResolveAgeFile_EnvHomeHonoursRedirect(t *testing.T) {
	got := ResolveAgeFile(source.SecretsConfig{File: "${env:HOME}/x.age"}, "/irrelevant", "/redirected/home")
	if got != "/redirected/home/x.age" {
		t.Fatalf("got %q, want /redirected/home/x.age", got)
	}
}
