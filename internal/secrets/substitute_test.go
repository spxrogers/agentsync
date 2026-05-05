package secrets_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestSubstituteCanonical_MCPEnv(t *testing.T) {
	sec := mapBackend{"github.token": "ghp_abc"}
	env := mapBackend{"MY_HOST": "localhost"}

	c := source.Canonical{
		MCPServers: []source.MCPServer{
			{
				ID: "github",
				Server: source.MCPServerSpec{
					Type:    "stdio",
					Command: "npx",
					Args:    []string{"-y", "@modelcontextprotocol/server-github"},
					Env: map[string]string{
						"GITHUB_TOKEN": "${secret:github.token}",
						"HOST":         "${env:MY_HOST}",
					},
				},
			},
		},
	}

	if err := secrets.SubstituteCanonical(&c, sec, env); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := c.MCPServers[0].Server
	if srv.Env["GITHUB_TOKEN"] != "ghp_abc" {
		t.Errorf("GITHUB_TOKEN = %q, want ghp_abc", srv.Env["GITHUB_TOKEN"])
	}
	if srv.Env["HOST"] != "localhost" {
		t.Errorf("HOST = %q, want localhost", srv.Env["HOST"])
	}
}

func TestSubstituteCanonical_UnresolvedReturnsError(t *testing.T) {
	sec := mapBackend{}   // empty — no secrets
	env := mapBackend{}   // empty

	c := source.Canonical{
		MCPServers: []source.MCPServer{
			{
				ID: "foo",
				Server: source.MCPServerSpec{
					Env: map[string]string{
						"TOKEN": "${secret:missing.token}",
					},
				},
			},
		},
	}

	err := secrets.SubstituteCanonical(&c, sec, env)
	if err == nil {
		t.Fatal("expected error for unresolved secret reference")
	}
}

func TestSubstituteCanonical_HookCommand(t *testing.T) {
	sec := mapBackend{"signing.key": "sk_live_xyz"}
	env := mapBackend{}

	c := source.Canonical{
		Hooks: []source.Hook{
			{Event: "PreToolUse", Command: "sign --key=${secret:signing.key}"},
		},
	}

	if err := secrets.SubstituteCanonical(&c, sec, env); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Hooks[0].Command != "sign --key=sk_live_xyz" {
		t.Errorf("hook command = %q, want resolved", c.Hooks[0].Command)
	}
}

func TestSubstituteCanonical_NoRefs(t *testing.T) {
	// Canonical with no refs should pass through unchanged.
	sec := secrets.NopResolver{}
	env := secrets.NopResolver{}

	c := source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "plain", Server: source.MCPServerSpec{Command: "npx", Env: map[string]string{"FOO": "bar"}}},
		},
	}

	if err := secrets.SubstituteCanonical(&c, sec, env); err != nil {
		t.Fatalf("unexpected error for canonical without refs: %v", err)
	}
	if c.MCPServers[0].Server.Env["FOO"] != "bar" {
		t.Errorf("plain env mutated unexpectedly")
	}
}
