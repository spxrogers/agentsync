package opencode_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// ---------------------------------------------------------------------------
// Task 4: MCP
// ---------------------------------------------------------------------------

func TestRender_MCP(t *testing.T) {
	enabled := true
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "github",
		Server: source.MCPServerSpec{
			Type: "stdio", Command: "npx", Args: []string{"-y", "x"},
			Env:    map[string]string{"TOKEN": "abc"},
			Agents: []string{"opencode"}, Enabled: &enabled,
		},
	}}}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	var found bool
	for _, op := range ops {
		if strings.HasSuffix(op.Path, "opencode.json") {
			found = true
			if op.MergeStrategy != "merge-jsonc-keys" {
				t.Fatalf("merge strategy = %q", op.MergeStrategy)
			}
			var ours map[string]any
			_ = json.Unmarshal(op.Content, &ours)
			mcp := ours["mcp"].(map[string]any)["github"].(map[string]any)
			// OpenCode's native schema: type local|remote, command as a string
			// ARRAY ([command, ...args]), and `environment` (not `env`).
			if mcp["type"] != "local" {
				t.Fatalf("type = %v, want \"local\"", mcp["type"])
			}
			cmd, ok := mcp["command"].([]any)
			if !ok || len(cmd) != 3 || cmd[0] != "npx" || cmd[1] != "-y" || cmd[2] != "x" {
				t.Fatalf("command = %v, want [\"npx\",\"-y\",\"x\"] array", mcp["command"])
			}
			if _, hasEnv := mcp["env"]; hasEnv {
				t.Fatalf("must use \"environment\", not \"env\": %v", mcp)
			}
			env, ok := mcp["environment"].(map[string]any)
			if !ok || env["TOKEN"] != "abc" {
				t.Fatalf("environment = %v, want {TOKEN: abc}", mcp["environment"])
			}
		}
	}
	if !found {
		t.Fatal("opencode.json op missing")
	}
}

// TestRender_MCP_ProjectScope verifies project-scope MCP servers target
// <project>/opencode.json — OpenCode's documented project config location at the
// repo ROOT — NOT <project>/.opencode/opencode.json (which OpenCode does not
// read; the .opencode/ directory holds only the agents/commands/skills subdirs).
func TestRender_MCP_ProjectScope(t *testing.T) {
	enabled := true
	projRoot := t.TempDir()
	srv := source.MCPServer{
		ID: "company-api",
		Server: source.MCPServerSpec{
			Type: "stdio", Command: "npx", Args: []string{"-y", "@company/mcp"},
			Agents: []string{"opencode"}, Enabled: &enabled,
		},
	}
	// At project scope apply renders only the project overlay (c.Project).
	projCanon := source.Canonical{MCPServers: []source.MCPServer{srv}}
	merged := source.Canonical{MCPServers: []source.MCPServer{srv}, Project: &projCanon}

	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(merged), adapter.ScopeProject, projRoot)
	if err != nil {
		t.Fatal(err)
	}

	wantMCP := filepath.Join(projRoot, "opencode.json")
	wrongMCP := filepath.Join(projRoot, ".opencode", "opencode.json")
	var found bool
	for _, op := range ops {
		if op.Path == wrongMCP {
			t.Fatalf("project MCP must not land in .opencode/opencode.json (OpenCode does not read it): %s", op.Path)
		}
		if op.Path == wantMCP {
			found = true
			if op.MergeStrategy != "merge-jsonc-keys" {
				t.Fatalf("merge strategy = %q", op.MergeStrategy)
			}
			var ours map[string]any
			_ = json.Unmarshal(op.Content, &ours)
			if _, ok := ours["mcp"].(map[string]any)["company-api"]; !ok {
				t.Fatalf("company-api missing under mcp: %v", ours)
			}
		}
	}
	if !found {
		t.Fatalf("no <project>/opencode.json op produced; ops=%+v", ops)
	}
}

// TestRender_MCP_PreservesHeaders is the regression for OpenCode silently
// dropping the `headers` of a remote MCP server — a server requiring an
// Authorization header would render unauthenticated and broken, with no Skip.
func TestRender_MCP_PreservesHeaders(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "remote",
		Server: source.MCPServerSpec{
			Type: "http", URL: "https://mcp.example.com",
			Headers: map[string]string{"Authorization": "Bearer tok"},
		},
	}}}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	for _, op := range ops {
		if !strings.HasSuffix(op.Path, "opencode.json") {
			continue
		}
		var ours map[string]any
		_ = json.Unmarshal(op.Content, &ours)
		mcp := ours["mcp"].(map[string]any)["remote"].(map[string]any)
		if mcp["type"] != "remote" {
			t.Fatalf("type = %v, want \"remote\"", mcp["type"])
		}
		if mcp["url"] != "https://mcp.example.com" {
			t.Fatalf("url = %v", mcp["url"])
		}
		h, ok := mcp["headers"].(map[string]any)
		if !ok || h["Authorization"] != "Bearer tok" {
			t.Fatalf("opencode MCP dropped headers: %v", mcp)
		}
		return
	}
	t.Fatal("opencode.json op missing")
}

func TestRender_MCP_SkipsDisabled(t *testing.T) {
	disabled := false
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "disabled-srv",
		Server: source.MCPServerSpec{
			Command: "npx", Enabled: &disabled,
		},
	}}}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	for _, op := range ops {
		if strings.HasSuffix(op.Path, "opencode.json") {
			t.Fatal("should not emit op for disabled server")
		}
	}
}

func TestRender_MCP_SkipsOtherAgents(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "claude-only",
		Server: source.MCPServerSpec{
			Command: "npx", Agents: []string{"claude"},
		},
	}}}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	for _, op := range ops {
		if strings.HasSuffix(op.Path, "opencode.json") {
			t.Fatal("should not emit op for claude-only server")
		}
	}
}

// ---------------------------------------------------------------------------
// Task 5: Memory
// ---------------------------------------------------------------------------

func TestRender_Memory(t *testing.T) {
	c := source.Canonical{
		Memory: source.Memory{Body: "# Agent memory\n\nThis is the memory.\n"},
	}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	var found bool
	for _, op := range ops {
		if strings.HasSuffix(op.Path, "AGENTS.md") {
			found = true
			if !strings.Contains(string(op.Content), "Agent memory") {
				t.Fatalf("memory body not written: %s", op.Content)
			}
			if op.MergeStrategy != "replace" {
				t.Fatalf("merge strategy = %q; want replace", op.MergeStrategy)
			}
		}
	}
	if !found {
		t.Fatal("AGENTS.md op missing")
	}
}

func TestRender_Memory_Empty(t *testing.T) {
	c := source.Canonical{}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	for _, op := range ops {
		if strings.HasSuffix(op.Path, "AGENTS.md") {
			t.Fatal("should not emit AGENTS.md op when memory is empty")
		}
	}
}

func TestRender_Memory_FragmentExpansion(t *testing.T) {
	c := source.Canonical{
		Memory: source.Memory{
			Body:      "# Main\n\n@import ./fragments/intro.md\n",
			Fragments: map[string]string{"intro.md": "Hello from fragment."},
		},
	}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	for _, op := range ops {
		if strings.HasSuffix(op.Path, "AGENTS.md") {
			if strings.Contains(string(op.Content), "@import") {
				t.Fatalf("@import not expanded: %s", op.Content)
			}
			if !strings.Contains(string(op.Content), "Hello from fragment") {
				t.Fatalf("fragment content missing: %s", op.Content)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Task 6: Skills
// ---------------------------------------------------------------------------

func TestRender_Skill(t *testing.T) {
	c := source.Canonical{Skills: []source.Skill{{
		Name:        "my-skill",
		Frontmatter: map[string]any{"description": "My skill"},
		Body:        "Skill body.\n",
	}}}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	var found bool
	for _, op := range ops {
		if strings.HasSuffix(op.Path, "SKILL.md") && strings.Contains(op.Path, "my-skill") {
			found = true
			if !strings.Contains(op.Path, ".claude/skills") {
				t.Fatalf("skill not in shared .claude/skills path: %s", op.Path)
			}
			if !strings.Contains(string(op.Content), "Skill body") {
				t.Fatalf("skill body missing: %s", op.Content)
			}
		}
	}
	if !found {
		t.Fatal("SKILL.md op missing")
	}
}

// ---------------------------------------------------------------------------
// Task 7: Subagents
// ---------------------------------------------------------------------------

func TestRender_Subagent_FrontmatterMunge(t *testing.T) {
	c := source.Canonical{Subagents: []source.Subagent{{
		Name: "review",
		Frontmatter: map[string]any{
			"description": "Code review",
			"model":       "claude-sonnet-4-7",
			"tools":       []string{"Read", "Grep"},
			"color":       "blue",
		},
		Body: "Review code.\n",
	}}}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, skips, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	// verify file content
	var op *adapter.FileOp
	for i, o := range ops {
		if strings.HasSuffix(o.Path, "/agents/review.md") {
			op = &ops[i]
		}
	}
	if op == nil {
		t.Fatal("no agent op")
	}
	if !strings.Contains(string(op.Content), "mode: subagent") {
		t.Fatalf("missing mode:subagent in: %s", op.Content)
	}
	if strings.Contains(string(op.Content), "color:") {
		t.Fatalf("color should be dropped: %s", op.Content)
	}
	if strings.Contains(string(op.Content), "tools:") {
		t.Fatalf("tools should be dropped: %s", op.Content)
	}
	// verify skip log
	var sawToolsSkip bool
	for _, s := range skips {
		if s.Component == "subagent-frontmatter" && s.Name == "review" {
			if strings.Contains(s.Reason, "tools") {
				sawToolsSkip = true
			}
		}
	}
	if !sawToolsSkip {
		t.Fatalf("no skip emitted for tools allowlist")
	}
}

func TestRender_Subagent_PreservesDescriptionAndModel(t *testing.T) {
	c := source.Canonical{Subagents: []source.Subagent{{
		Name: "helper",
		Frontmatter: map[string]any{
			"description": "Helpful agent",
			"model":       "claude-opus-4-5",
		},
		Body: "Help with things.\n",
	}}}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	var found bool
	for _, op := range ops {
		if strings.HasSuffix(op.Path, "/agents/helper.md") {
			found = true
			content := string(op.Content)
			if !strings.Contains(content, "Helpful agent") {
				t.Fatalf("description missing: %s", content)
			}
			if !strings.Contains(content, "claude-opus-4-5") {
				t.Fatalf("model missing: %s", content)
			}
		}
	}
	if !found {
		t.Fatal("helper.md op missing")
	}
}

// ---------------------------------------------------------------------------
// Task 8: Commands
// ---------------------------------------------------------------------------

func TestRender_Command_FrontmatterMunge(t *testing.T) {
	c := source.Canonical{Commands: []source.Command{{
		Name: "summarize",
		Frontmatter: map[string]any{
			"description":   "Summarize code",
			"argument-hint": "<file>",
			"model":         "claude-sonnet-4-7",
		},
		Body: "Summarize the given file.\n",
	}}}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, skips, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	var op *adapter.FileOp
	for i, o := range ops {
		if strings.HasSuffix(o.Path, "/commands/summarize.md") {
			op = &ops[i]
		}
	}
	if op == nil {
		t.Fatal("no command op")
	}
	content := string(op.Content)
	if !strings.Contains(content, "description: Summarize code") {
		t.Fatalf("description missing: %s", content)
	}
	if !strings.Contains(content, "claude-sonnet-4-7") {
		t.Fatalf("model missing: %s", content)
	}
	if strings.Contains(content, "argument-hint") {
		t.Fatalf("argument-hint should be dropped: %s", content)
	}
	// verify skip for argument-hint
	var sawHintSkip bool
	for _, s := range skips {
		if s.Component == "command-frontmatter" && s.Name == "summarize" {
			if strings.Contains(s.Reason, "argument-hint") {
				sawHintSkip = true
			}
		}
	}
	if !sawHintSkip {
		t.Fatalf("no skip emitted for argument-hint")
	}
}

func TestRender_Command_BodyPreserved(t *testing.T) {
	c := source.Canonical{Commands: []source.Command{{
		Name:        "lint",
		Frontmatter: map[string]any{"description": "Run linter"},
		Body:        "Run the linter on $ARGUMENTS.\n",
	}}}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	var found bool
	for _, op := range ops {
		if strings.HasSuffix(op.Path, "/commands/lint.md") {
			found = true
			if !strings.Contains(string(op.Content), "Run the linter") {
				t.Fatalf("body missing: %s", op.Content)
			}
		}
	}
	if !found {
		t.Fatal("lint.md op missing")
	}
}

// ---------------------------------------------------------------------------
// Project scope — only project items rendered
// ---------------------------------------------------------------------------

// TestRender_ProjectScope_OnlyProjectItems verifies that at project scope the
// adapter renders only the project-overlay items (c.Project), not the merged
// canonical that also includes user-scope items.
func TestRender_ProjectScope_OnlyProjectItems(t *testing.T) {
	projSkill := source.Skill{
		Name:        "proj-skill",
		Frontmatter: map[string]any{"name": "proj-skill", "description": "project skill"},
		Body:        "Project skill.\n",
	}
	projRoot := t.TempDir()
	projCanon := source.Canonical{Skills: []source.Skill{projSkill}}
	merged := source.Canonical{
		Skills: []source.Skill{
			{Name: "user-skill", Frontmatter: map[string]any{"name": "user-skill"}, Body: "User skill.\n"},
			projSkill,
		},
		Project: &projCanon,
	}

	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(merged), adapter.ScopeProject, projRoot)
	if err != nil {
		t.Fatal(err)
	}

	// OpenCode shares Claude's skills dir (.claude/skills/) at project scope.
	wantExcluded := filepath.Join(projRoot, ".claude", "skills", "user-skill", "SKILL.md")
	wantProjSkill := filepath.Join(projRoot, ".claude", "skills", "proj-skill", "SKILL.md")

	var gotProjSkill bool
	for _, op := range ops {
		if op.Path == wantExcluded {
			t.Fatalf("user-scope skill must not be written at project scope: %s", op.Path)
		}
		if op.Path == wantProjSkill {
			gotProjSkill = true
		}
	}
	if !gotProjSkill {
		t.Fatalf("project skill not rendered; want op at %s, ops=%+v", wantProjSkill, ops)
	}
}

// ---------------------------------------------------------------------------
// Hooks + LSP skips
// ---------------------------------------------------------------------------

func TestRender_HooksAndLSP_Skipped(t *testing.T) {
	c := source.Canonical{
		Hooks:      []source.Hook{{Event: "PreToolUse", Command: "echo hi"}},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
	}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	_, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	var hookSkip, lspSkip bool
	for _, s := range skips {
		if s.Component == "hook" {
			hookSkip = true
		}
		if s.Component == "lsp" {
			lspSkip = true
		}
	}
	if !hookSkip {
		t.Fatal("expected hook skip")
	}
	if !lspSkip {
		t.Fatal("expected lsp skip")
	}
}
