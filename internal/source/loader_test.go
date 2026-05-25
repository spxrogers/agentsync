package source_test

import (
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

// TestParseFrontmatter_ClosingFenceAtEOF guards the parser against two common
// real-world shapes that previously returned "unterminated frontmatter":
// a closing "---" at end-of-file with no trailing newline (editors strip it),
// and an empty frontmatter block. Each must parse, not error.
func TestParseFrontmatter_ClosingFenceAtEOF(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantName any
		wantBody string
	}{
		{"fence at EOF no trailing newline", "---\nname: demo\n---", "demo", ""},
		{"fence at EOF with body no trailing newline", "---\nname: demo\n---\nbody", "demo", "body"},
		{"normal with trailing newline", "---\nname: demo\n---\nbody\n", "demo", "body\n"},
		{"empty frontmatter with body", "---\n---\nbody\n", nil, "body\n"},
		{"empty frontmatter at EOF", "---\n---", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fm, body, err := source.ParseFrontmatter([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseFrontmatter(%q) = error %v; want success", tc.in, err)
			}
			if fm["name"] != tc.wantName {
				t.Errorf("name = %v, want %v", fm["name"], tc.wantName)
			}
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

// TestLoad_SkillWithFenceAtEOF proves the parser bug's blast radius: source.Load
// is the entry point for every command, so a single benign SKILL.md whose
// closing fence sits at EOF must not abort the whole load.
func TestLoad_SkillWithFenceAtEOF(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/home/.agentsync/agentsync.toml", []byte(""), 0o644)
	// No trailing newline after the closing fence.
	_ = afero.WriteFile(fs, "/home/.agentsync/skills/demo/SKILL.md",
		[]byte("---\nname: demo\ndescription: x\n---"), 0o644)
	c, err := source.Load(fs, "/home/.agentsync")
	if err != nil {
		t.Fatalf("Load aborted on a benign skill file: %v", err)
	}
	if len(c.Skills) != 1 || c.Skills[0].Name != "demo" {
		t.Fatalf("skill not loaded: %+v", c.Skills)
	}
}

// TestLoad_NestedMemoryFragments proves fragments in subdirectories are loaded
// and keyed by their path under memory/fragments/, matching the @import regex
// which accepts "./fragments/<name>" where <name> may contain "/". Previously
// the loader read fragments non-recursively (basename only), so a nested
// fragment was never loaded and its @import silently stayed literal.
func TestLoad_NestedMemoryFragments(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/home/.agentsync/agentsync.toml", []byte(""), 0o644)
	_ = afero.WriteFile(fs, "/home/.agentsync/memory/AGENTS.md", []byte("Top\n@import ./fragments/sub/frag.md\n"), 0o644)
	_ = afero.WriteFile(fs, "/home/.agentsync/memory/fragments/sub/frag.md", []byte("nested body"), 0o644)
	_ = afero.WriteFile(fs, "/home/.agentsync/memory/fragments/top.md", []byte("top body"), 0o644)

	c, err := source.Load(fs, "/home/.agentsync")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, ok := c.Memory.Fragments["sub/frag.md"]; !ok || got != "nested body" {
		t.Fatalf("nested fragment not loaded under its sub-path: %#v", c.Memory.Fragments)
	}
	if got, ok := c.Memory.Fragments["top.md"]; !ok || got != "top body" {
		t.Fatalf("flat fragment regressed: %#v", c.Memory.Fragments)
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
