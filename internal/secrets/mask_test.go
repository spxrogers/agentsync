package secrets_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestCollectResolved_MasksJSONEscapedSecret is the regression for the diff
// credential-leak when a secret value contains JSON-special characters (a
// quote, backslash, or control char — common in GCP service-account JSON
// keys, escaped tokens, base64 blobs). apply substitutes the RAW value into
// .claude.json, where it is stored JSON-ESCAPED. diff reads it back and
// JSON-marshals it before masking, so a redaction map keyed only on the raw
// value never matches the escaped on-disk form and the cleartext leaks to
// stdout. CollectResolved must register the JSON-escaped representation too.
func TestCollectResolved_MasksJSONEscapedSecret(t *testing.T) {
	raw := `tok"en\val` // contains a double-quote and a backslash
	sec := mapBackend{"gcp.key": raw}
	env := mapBackend{}
	c := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "x",
			Server: source.MCPServerSpec{
				Env: map[string]string{"GCP_KEY": "${secret:gcp.key}"},
			},
		}},
	}
	redact := secrets.CollectResolved(&c, sec, env)

	// Simulate what diff prints: the value as it sits in the on-disk JSON,
	// i.e. json.Marshal of the raw value (quotes + escaping).
	escaped, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	masked := secrets.MaskResolved(string(escaped), redact)
	if strings.Contains(masked, `en\val`) {
		t.Fatalf("JSON-escaped secret leaked through diff redaction: %s", masked)
	}
	if !strings.Contains(masked, "${secret:gcp.key}") {
		t.Fatalf("placeholder missing after masking escaped form: %s", masked)
	}
}

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
// diff-leak: CollectResolved silently skips refs the backend cannot resolve, so
// the cleartext value already on disk would print unredacted.
// UnresolvedSecretRefs surfaces those keys so `diff` can fail closed instead of
// leaking. Both ${secret:…} (locked/absent key) and ${env:…} (var unset now but
// set at apply time) must be flagged.
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
	// sec resolves github.token but NOT other.key; env resolves nothing (MY_HOST
	// unset) — mimics a partially-available backend and an env var unset since
	// apply.
	sec := mapBackend{"github.token": "ghp_x"}
	env := mapBackend{}
	got := secrets.UnresolvedSecretRefs(&c, sec, env)
	want := []string{"env:MY_HOST", "secret:other.key"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("want %v, got %v", want, got)
	}
}

// TestUnresolvedSecretRefs_NoneWhenAllResolve confirms the common case does not
// trip the fail-closed guard: every ${secret:…} AND ${env:…} resolves.
func TestUnresolvedSecretRefs_NoneWhenAllResolve(t *testing.T) {
	c := source.Canonical{
		Hooks: []source.Hook{{Command: "echo ${secret:a} ${env:B}"}},
	}
	sec := mapBackend{"a": "secret-a"}
	env := mapBackend{"B": "env-b"}
	if got := secrets.UnresolvedSecretRefs(&c, sec, env); len(got) != 0 {
		t.Fatalf("want none, got %v", got)
	}
}
