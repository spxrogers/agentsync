package source_test

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestLoad_EmptyHome(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/home/.agentsync/agentsync.toml", []byte(""), 0o644)

	c, err := source.Load(fs, "/home/.agentsync")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.MCPServers) != 0 {
		t.Fatalf("expected no MCP servers, got %d", len(c.MCPServers))
	}
}

func TestLoad_AgentsAndMCP(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/home/.agentsync/agentsync.toml", []byte(`
[agents]
claude   = { enabled = true,  scope = "user" }
opencode = { enabled = true }
`), 0o644)
	_ = afero.WriteFile(fs, "/home/.agentsync/mcp/github.toml", []byte(`
[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
agents  = ["claude", "opencode"]

[server.env]
GITHUB_TOKEN = "${secret:github.token}"
`), 0o644)

	c, err := source.Load(fs, "/home/.agentsync")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Config.Agents["claude"].Enabled {
		t.Fatalf("claude agent should be enabled")
	}
	if c.Config.Agents["claude"].Scope != "user" {
		t.Fatalf("claude scope = %q, want user", c.Config.Agents["claude"].Scope)
	}
	if len(c.MCPServers) != 1 {
		t.Fatalf("expected 1 MCP server, got %d", len(c.MCPServers))
	}
	g := c.MCPServers[0]
	if g.ID != "github" {
		t.Fatalf("MCP id = %q, want github", g.ID)
	}
	if g.Server.Type != "stdio" {
		t.Fatalf("MCP type = %q, want stdio", g.Server.Type)
	}
	if g.Server.Env["GITHUB_TOKEN"] != "${secret:github.token}" {
		t.Fatalf("env GITHUB_TOKEN = %q", g.Server.Env["GITHUB_TOKEN"])
	}
}

func TestLoad_MissingHomeReturnsEmpty(t *testing.T) {
	fs := afero.NewMemMapFs()
	c, err := source.Load(fs, "/nonexistent")
	if err != nil {
		t.Fatalf("Load nonexistent: %v", err)
	}
	if len(c.MCPServers) != 0 {
		t.Fatalf("expected empty canonical, got %d MCP", len(c.MCPServers))
	}
}
