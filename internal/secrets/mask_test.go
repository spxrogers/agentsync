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
