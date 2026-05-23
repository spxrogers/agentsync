package secrets_test

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestCollectResolved_MCPEnv(t *testing.T) {
	sec := mapBackend{"github.token": "ghp_SENTINEL_TOKEN_DO_NOT_LEAK"}
	env := mapBackend{"MY_HOST": "localhost.test"}

	c := source.Canonical{
		MCPServers: []source.MCPServer{
			{
				ID: "github",
				Server: source.MCPServerSpec{
					Env: map[string]string{
						"GITHUB_TOKEN": "${secret:github.token}",
						"HOST":         "${env:MY_HOST}",
					},
				},
			},
		},
	}
	got := secrets.CollectResolved(&c, sec, env)
	if got["ghp_SENTINEL_TOKEN_DO_NOT_LEAK"] != "${secret:github.token}" {
		t.Errorf("missing token mapping: %v", got)
	}
	if got["localhost.test"] != "${env:MY_HOST}" {
		t.Errorf("missing env mapping: %v", got)
	}
}

func TestMaskResolved_RedactsSentinelToken(t *testing.T) {
	resolved := map[string]string{
		"ghp_SENTINEL_TOKEN_DO_NOT_LEAK": "${secret:github.token}",
	}
	in := `{"command":"gh","env":{"GH_TOKEN":"ghp_SENTINEL_TOKEN_DO_NOT_LEAK"}}`
	got := secrets.MaskResolved(in, resolved)
	if strings.Contains(got, "ghp_SENTINEL_TOKEN_DO_NOT_LEAK") {
		t.Fatalf("sentinel token leaked: %s", got)
	}
	if !strings.Contains(got, "${secret:github.token}") {
		t.Fatalf("placeholder missing: %s", got)
	}
}

// TestMaskResolved_LongestMatchFirst ensures that when one resolved value
// is a substring of another, the longer match is applied first so we
// don't get a partial-replacement leak.
func TestMaskResolved_LongestMatchFirst(t *testing.T) {
	resolved := map[string]string{
		"abc":        "${env:SHORT}",
		"abc-prefix": "${secret:long.token}",
	}
	got := secrets.MaskResolved("abc-prefix is set, abc is set", resolved)
	if strings.Contains(got, "abc-prefix") {
		t.Fatalf("longer value leaked into output: %s", got)
	}
}

func TestMaskResolved_NoLeakWhenNothingResolved(t *testing.T) {
	got := secrets.MaskResolved("nothing to redact", nil)
	if got != "nothing to redact" {
		t.Fatalf("mask with nil map mutated input: %s", got)
	}
}

func TestCollectResolved_SkipsUnresolved(t *testing.T) {
	sec := mapBackend{} // empty — nothing to resolve
	env := mapBackend{}
	c := source.Canonical{
		Hooks: []source.Hook{
			{Command: "echo ${secret:missing}"},
		},
	}
	got := secrets.CollectResolved(&c, sec, env)
	if len(got) != 0 {
		t.Fatalf("expected no entries for unresolvable refs, got %v", got)
	}
}

// TestUnresolvedSecretRefs_FlagsUnresolvable is the regression for the
// diff-leak: CollectResolved silently skips ${secret:…} refs the backend
// cannot resolve (age key locked/absent), so the cleartext value already on
// disk would print unredacted. UnresolvedSecretRefs surfaces those keys so
// `diff` can fail closed instead of leaking. ${env:…} refs are intentionally
// ignored — the env backend is always available and is not a leak risk.
func TestUnresolvedSecretRefs_FlagsUnresolvable(t *testing.T) {
	c := source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "github", Server: source.MCPServerSpec{
				Env: map[string]string{
					"GITHUB_TOKEN": "${secret:github.token}",
					"HOST":         "${env:MY_HOST}",
				},
			}},
		},
		Hooks: []source.Hook{{Command: "echo ${secret:other.key}"}},
	}
	// Backend resolves github.token but NOT other.key (mimics a partially
	// available backend / locked identity for some keys).
	sec := mapBackend{"github.token": "ghp_x"}
	got := secrets.UnresolvedSecretRefs(&c, sec)
	if len(got) != 1 || got[0] != "other.key" {
		t.Fatalf("want [other.key], got %v", got)
	}
}

// TestUnresolvedSecretRefs_NoneWhenAllResolve confirms the common case does
// not trip the fail-closed guard: every ${secret:…} resolves, and ${env:…}
// refs are ignored entirely.
func TestUnresolvedSecretRefs_NoneWhenAllResolve(t *testing.T) {
	c := source.Canonical{
		Hooks: []source.Hook{{Command: "echo ${secret:a} ${env:B}"}},
	}
	sec := mapBackend{"a": "secret-a"}
	if got := secrets.UnresolvedSecretRefs(&c, sec); len(got) != 0 {
		t.Fatalf("want none, got %v", got)
	}
}
