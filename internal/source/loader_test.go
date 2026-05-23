package source_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestLoad_AgentsyncTOMLRejectsTypos guards against the silent-drop bug
// where misspelled top-level keys in agentsync.toml were ignored and the
// default applied. Strict mode catches typos at load time.
func TestLoad_AgentsyncTOMLRejectsTypos(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/home/.agentsync/agentsync.toml", []byte(`
[updates]
defauls_mode = "track"
`), 0o644)
	_, err := source.Load(fs, "/home/.agentsync")
	if err == nil {
		t.Fatal("expected error from typo'd 'defauls_mode' key")
	}
	// We don't pin on the exact pelletier message; just confirm the bad
	// key surfaces.
	if got := err.Error(); !contains(got, "defauls_mode") {
		t.Fatalf("error %q does not mention the typo'd key", got)
	}
}

// contains is a small substring helper so the new test reads cleanly
// without bringing in strings.Contains for one call.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

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

func TestLoad_SkillFrontmatter(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/home/.agentsync/skills/foo/SKILL.md", []byte(`---
name: foo
description: Test skill
---
Body.
`), 0o644)

	c, err := source.Load(fs, "/home/.agentsync")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Skills) != 1 {
		t.Fatalf("skills = %d", len(c.Skills))
	}
	s := c.Skills[0]
	if s.Frontmatter["name"] != "foo" {
		t.Fatalf("frontmatter name = %v", s.Frontmatter["name"])
	}
	if s.Body != "Body.\n" {
		t.Fatalf("body = %q", s.Body)
	}
}

func TestLoad_Subagents(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/home/.agentsync/agents/reviewer.md", []byte(`---
description: Code reviewer subagent
tools:
  - read
  - edit
model: claude-opus-4-5
---
You are a code reviewer.
`), 0o644)

	c, err := source.Load(fs, "/home/.agentsync")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Subagents) != 1 {
		t.Fatalf("subagents = %d, want 1", len(c.Subagents))
	}
	sa := c.Subagents[0]
	if sa.Name != "reviewer" {
		t.Fatalf("name = %q, want reviewer", sa.Name)
	}
	if sa.Frontmatter["description"] != "Code reviewer subagent" {
		t.Fatalf("description = %v", sa.Frontmatter["description"])
	}
	if sa.Body != "You are a code reviewer.\n" {
		t.Fatalf("body = %q", sa.Body)
	}
}

func TestLoad_Commands(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/home/.agentsync/commands/review.md", []byte(`---
description: Run a code review
---
Please review the current changes.
`), 0o644)

	c, err := source.Load(fs, "/home/.agentsync")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(c.Commands))
	}
	cmd := c.Commands[0]
	if cmd.Name != "review" {
		t.Fatalf("name = %q, want review", cmd.Name)
	}
	if cmd.Frontmatter["description"] != "Run a code review" {
		t.Fatalf("description = %v", cmd.Frontmatter["description"])
	}
}

func TestLoad_Hooks(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/home/.agentsync/hooks/PreToolUse.toml", []byte(`
[[hook]]
matcher = "Write|Edit"
type    = "command"
command = "echo intercepting Write/Edit"

[[hook]]
matcher = "Bash"
type    = "command"
command = "echo intercepting Bash"
`), 0o644)

	c, err := source.Load(fs, "/home/.agentsync")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Hooks) != 2 {
		t.Fatalf("hooks = %d, want 2", len(c.Hooks))
	}
	for _, h := range c.Hooks {
		if h.Event != "PreToolUse" {
			t.Fatalf("event = %q, want PreToolUse", h.Event)
		}
	}
	if c.Hooks[0].Matcher != "Write|Edit" {
		t.Fatalf("matcher = %q", c.Hooks[0].Matcher)
	}
}

func TestLoad_LSPServers(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/home/.agentsync/lsp/gopls.toml", []byte(`
[server]
command = "gopls"
args    = ["-mode=stdio"]
`), 0o644)

	c, err := source.Load(fs, "/home/.agentsync")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.LSPServers) != 1 {
		t.Fatalf("lsp servers = %d, want 1", len(c.LSPServers))
	}
	lsp := c.LSPServers[0]
	if lsp.ID != "gopls" {
		t.Fatalf("id = %q, want gopls", lsp.ID)
	}
	if lsp.Spec.Command != "gopls" {
		t.Fatalf("command = %q", lsp.Spec.Command)
	}
}

func TestLoad_PluginExpandsToMCP(t *testing.T) {
	fs := afero.NewMemMapFs()
	home := "/h"
	cache := "/h/.state/cache/plugins"
	_ = afero.WriteFile(fs, filepath.Join(home, "plugins", "x.toml"), []byte(`
[plugin]
id = "x@m"
version = "1"
`), 0o644)
	_ = afero.WriteFile(fs, filepath.Join(cache, "x", ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"x","mcpServers":{"server-from-plugin":{"command":"x"}}}`),
		0o644)
	c, err := source.LoadWithCache(fs, home, cache)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range c.MCPServers {
		if m.ID == "server-from-plugin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plugin's MCP not surfaced via projection: %+v", c.MCPServers)
	}
}

// TestLoad_RejectsEscapingComponentPath is the regression for the
// manifest-path traversal on the loader projection path: a hostile
// plugin.json listing a skills/commands/agents path outside the plugin
// cache must be refused rather than read and projected.
func TestLoad_RejectsEscapingComponentPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	home := "/h"
	cache := "/h/.state/cache/plugins"
	_ = afero.WriteFile(fs, filepath.Join(home, "plugins", "x.toml"), []byte(`
[plugin]
id = "x@m"
version = "1"
`), 0o644)
	_ = afero.WriteFile(fs, filepath.Join(cache, "x", ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"x","skills":["../../../../etc/passwd"]}`),
		0o644)
	_, err := source.LoadWithCache(fs, home, cache)
	if err == nil {
		t.Fatal("expected error for plugin.json skill path escaping the cache")
	}
	if !strings.Contains(err.Error(), "escapes plugin cache") {
		t.Fatalf("error should explain the escape; got: %v", err)
	}
}

// TestLoad_PluginManifestSHAMismatchRefuses is the regression for the
// finding that ManifestSHA was recorded at install but never verified
// at load. A user installs a plugin, the cache gets tampered with
// (deliberate hand-edit or upstream compromise), and the next apply
// silently projects the modified MCP/hook/skill content with the
// original SHA still in plugins/<id>.toml.
func TestLoad_PluginManifestSHAMismatchRefuses(t *testing.T) {
	fs := afero.NewMemMapFs()
	home := "/h-tamper"
	cache := "/h-tamper/.state/cache/plugins"
	// Pin a SHA that intentionally does not match the cached manifest.
	_ = afero.WriteFile(fs, filepath.Join(home, "plugins", "tamper.toml"), []byte(`
[plugin]
id = "tamper@m"
version = "1"
manifest_sha = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
`), 0o644)
	_ = afero.WriteFile(fs, filepath.Join(cache, "tamper", ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"tamper","mcpServers":{"backdoor":{"command":"evil"}}}`),
		0o644)
	_, err := source.LoadWithCache(fs, home, cache)
	if err == nil {
		t.Fatal("expected SHA-mismatch error, got nil")
	}
	if !contains(err.Error(), "manifest SHA mismatch") {
		t.Fatalf("error %q does not mention SHA mismatch", err.Error())
	}
}

// TestLoad_PluginManifestSHAOverrideEnv proves the documented escape
// hatch (AGENTSYNC_ALLOW_PLUGIN_DRIFT=1) bypasses the check so users
// who have hand-edited the cache deliberately can roll forward without
// reinstalling.
func TestLoad_PluginManifestSHAOverrideEnv(t *testing.T) {
	t.Setenv("AGENTSYNC_ALLOW_PLUGIN_DRIFT", "1")
	fs := afero.NewMemMapFs()
	home := "/h-override"
	cache := "/h-override/.state/cache/plugins"
	_ = afero.WriteFile(fs, filepath.Join(home, "plugins", "ok.toml"), []byte(`
[plugin]
id = "ok@m"
version = "1"
manifest_sha = "wrong-sha-still-accepted-because-env"
`), 0o644)
	_ = afero.WriteFile(fs, filepath.Join(cache, "ok", ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"ok","mcpServers":{"a":{"command":"x"}}}`),
		0o644)
	c, err := source.LoadWithCache(fs, home, cache)
	if err != nil {
		t.Fatalf("env override should bypass SHA check: %v", err)
	}
	if len(c.MCPServers) == 0 {
		t.Fatalf("expected MCP servers projected under override; got none")
	}
}

// TestLoad_WithCacheNoPlugin verifies that LoadWithCache with a cacheDir but no
// plugin.json does not error and returns an empty projection (graceful skip).
func TestLoad_WithCacheNoPlugin(t *testing.T) {
	fs := afero.NewMemMapFs()
	home := "/h2"
	cache := "/h2/.state/cache/plugins"
	_ = afero.WriteFile(fs, filepath.Join(home, "plugins", "ghost.toml"), []byte(`
[plugin]
id = "ghost@m"
version = "0"
`), 0o644)
	// No plugin.json in cache — should not error.
	c, err := source.LoadWithCache(fs, home, cache)
	if err != nil {
		t.Fatalf("expected no error when plugin.json absent: %v", err)
	}
	if len(c.MCPServers) != 0 {
		t.Fatalf("expected no MCP servers from missing cache, got %d", len(c.MCPServers))
	}
}

// TestLoad_NoCacheDirBehavesLikeLoad verifies that LoadWithCache("") == Load.
func TestLoad_NoCacheDirBehavesLikeLoad(t *testing.T) {
	fs := afero.NewMemMapFs()
	home := "/h3"
	_ = afero.WriteFile(fs, filepath.Join(home, "mcp", "s.toml"), []byte(`
[server]
type = "stdio"
command = "s"
`), 0o644)
	c1, err1 := source.Load(fs, home)
	c2, err2 := source.LoadWithCache(fs, home, "")
	if err1 != nil || err2 != nil {
		t.Fatalf("errors: %v / %v", err1, err2)
	}
	if len(c1.MCPServers) != len(c2.MCPServers) {
		t.Fatalf("MCP count differs: Load=%d LoadWithCache=%d", len(c1.MCPServers), len(c2.MCPServers))
	}
}
