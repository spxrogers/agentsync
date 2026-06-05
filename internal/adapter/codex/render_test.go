package codex_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestProjectScope_EmptyProjectErrors pins the adapter-boundary guard for Codex:
// a project-scope Render/Ingest with no project root must fail loudly
// (adapter.ErrProjectRootRequired) rather than silently fall through to the
// user-scope ~/.codex/ paths.
func TestProjectScope_EmptyProjectErrors(t *testing.T) {
	enabled := true
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "x",
		Server: source.MCPServerSpec{Command: "y", Enabled: &enabled},
	}}}
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	if _, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Render: want ErrProjectRootRequired, got %v", err)
	}
	if _, err := a.Ingest(adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Ingest: want ErrProjectRootRequired, got %v", err)
	}
}

// findOp returns the first op whose path ends with suffix.
func findOp(ops []adapter.FileOp, suffix string) *adapter.FileOp {
	for i := range ops {
		if strings.HasSuffix(ops[i].Path, suffix) {
			return &ops[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// MCP
// ---------------------------------------------------------------------------

func TestRender_MCP_StdioToTOMLStrategy(t *testing.T) {
	enabled := true
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "github",
		Server: source.MCPServerSpec{
			Type: "stdio", Command: "npx", Args: []string{"-y", "x"},
			Env: map[string]string{"TOKEN": "abc"}, Agents: []string{"codex"}, Enabled: &enabled,
		},
	}}}
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, "config.toml")
	if op == nil {
		t.Fatal("config.toml op missing")
	}
	if op.MergeStrategy != "merge-toml-keys" {
		t.Fatalf("merge strategy = %q, want merge-toml-keys", op.MergeStrategy)
	}
	// op.Content is JSON (the pipeline's pointer-merge currency), not TOML.
	var ours map[string]any
	if err := json.Unmarshal(op.Content, &ours); err != nil {
		t.Fatalf("op.Content is not JSON: %v\n%s", err, op.Content)
	}
	srv := ours["mcp_servers"].(map[string]any)["github"].(map[string]any)
	if srv["command"] != "npx" {
		t.Fatalf("command = %v, want npx", srv["command"])
	}
	args, ok := srv["args"].([]any)
	if !ok || len(args) != 2 || args[0] != "-y" || args[1] != "x" {
		t.Fatalf("args = %v, want [-y x]", srv["args"])
	}
	env, ok := srv["env"].(map[string]any)
	if !ok || env["TOKEN"] != "abc" {
		t.Fatalf("env = %v, want {TOKEN: abc}", srv["env"])
	}
}

func TestRender_MCP_RemoteHeaders(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "figma",
		Server: source.MCPServerSpec{
			Type: "http", URL: "https://mcp.figma.com/mcp",
			Headers: map[string]string{"X-Region": "us"},
		},
	}}}
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, "config.toml")
	if op == nil {
		t.Fatal("config.toml op missing")
	}
	var ours map[string]any
	_ = json.Unmarshal(op.Content, &ours)
	srv := ours["mcp_servers"].(map[string]any)["figma"].(map[string]any)
	if srv["url"] != "https://mcp.figma.com/mcp" {
		t.Fatalf("url = %v", srv["url"])
	}
	if _, hasCmd := srv["command"]; hasCmd {
		t.Fatalf("remote server must not have command: %v", srv)
	}
	h, ok := srv["http_headers"].(map[string]any)
	if !ok || h["X-Region"] != "us" {
		t.Fatalf("http_headers dropped: %v", srv)
	}
}

func TestRender_MCP_SkipsDisabledAndOtherAgents(t *testing.T) {
	disabled := false
	c := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "off", Server: source.MCPServerSpec{Command: "x", Enabled: &disabled}},
		{ID: "claude-only", Server: source.MCPServerSpec{Command: "x", Agents: []string{"claude"}}},
	}}
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if op := findOp(ops, "config.toml"); op != nil {
		t.Fatalf("should emit no config.toml op for disabled/other-agent servers: %s", op.Content)
	}
}

// ---------------------------------------------------------------------------
// Memory
// ---------------------------------------------------------------------------

func TestRender_Memory(t *testing.T) {
	c := source.Canonical{Memory: source.Memory{Body: "# Memory\n\nBe helpful.\n"}}
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, "AGENTS.md")
	if op == nil {
		t.Fatal("AGENTS.md op missing")
	}
	if !strings.Contains(op.Path, ".codex") {
		t.Fatalf("memory should land under ~/.codex: %s", op.Path)
	}
	if op.MergeStrategy != "replace" {
		t.Fatalf("merge strategy = %q, want replace", op.MergeStrategy)
	}
	if !strings.Contains(string(op.Content), "Be helpful") {
		t.Fatalf("memory body missing: %s", op.Content)
	}
}

// ---------------------------------------------------------------------------
// Skill — lands in the shared ~/.agents/skills dir
// ---------------------------------------------------------------------------

func TestRender_Skill_SharedAgentsDir(t *testing.T) {
	c := source.Canonical{Skills: []source.Skill{{
		Name:        "my-skill",
		Frontmatter: map[string]any{"description": "My skill"},
		Body:        "Skill body.\n",
	}}}
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, "SKILL.md")
	if op == nil {
		t.Fatal("SKILL.md op missing")
	}
	if !strings.Contains(op.Path, ".agents/skills/my-skill") {
		t.Fatalf("skill not under ~/.agents/skills: %s", op.Path)
	}
	if !strings.Contains(string(op.Content), "Skill body") {
		t.Fatalf("skill body missing: %s", op.Content)
	}
}

// ---------------------------------------------------------------------------
// Subagent — markdown → TOML, tools/color dropped with a Skip
// ---------------------------------------------------------------------------

func TestRender_Subagent_ToTOML(t *testing.T) {
	c := source.Canonical{Subagents: []source.Subagent{{
		Name: "review",
		Frontmatter: map[string]any{
			"description": "Code review",
			"model":       "gpt-5.5",
			"tools":       []string{"Read", "Grep"},
			"color":       "blue",
		},
		Body: "Review the code.\n",
	}}}
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	ops, skips, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, "/agents/review.toml")
	if op == nil {
		t.Fatal("review.toml op missing")
	}
	content := string(op.Content)
	if !strings.Contains(content, `developer_instructions = `) || !strings.Contains(content, "Review the code") {
		t.Fatalf("body not under developer_instructions: %s", content)
	}
	if !strings.Contains(content, "Code review") || !strings.Contains(content, "gpt-5.5") {
		t.Fatalf("description/model missing: %s", content)
	}
	if strings.Contains(content, "tools") || strings.Contains(content, "color") {
		t.Fatalf("tools/color should be dropped from TOML: %s", content)
	}
	var sawSkip bool
	for _, s := range skips {
		if s.Component == "subagent-frontmatter" && s.Name == "review" &&
			strings.Contains(s.Reason, "tools") && strings.Contains(s.Reason, "color") {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Fatalf("expected a subagent-frontmatter skip listing tools+color, got %+v", skips)
	}
}

// ---------------------------------------------------------------------------
// Command — full fidelity at user scope, skipped at project scope
// ---------------------------------------------------------------------------

func TestRender_Command_UserScope_PreservesArgumentHint(t *testing.T) {
	c := source.Canonical{Commands: []source.Command{{
		Name: "summarize",
		Frontmatter: map[string]any{
			"description":   "Summarize code",
			"argument-hint": "<file>",
		},
		Body: "Summarize $ARGUMENTS.\n",
	}}}
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, "/prompts/summarize.md")
	if op == nil {
		t.Fatal("summarize.md prompt op missing")
	}
	content := string(op.Content)
	// Codex prompts support argument-hint, so it must be preserved (unlike OpenCode).
	if !strings.Contains(content, "argument-hint") || !strings.Contains(content, "<file>") {
		t.Fatalf("argument-hint should be preserved for Codex prompts: %s", content)
	}
	if !strings.Contains(content, "Summarize $ARGUMENTS") {
		t.Fatalf("body missing: %s", content)
	}
}

func TestRender_Command_ProjectScope_Skipped(t *testing.T) {
	c := source.Canonical{Commands: []source.Command{{
		Name: "summarize", Frontmatter: map[string]any{"description": "x"}, Body: "y\n",
	}}}
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	ops, skips, _ := a.Render(secrets.ForRender(c), adapter.ScopeProject, "/proj")
	if op := findOp(ops, "summarize.md"); op != nil {
		t.Fatalf("project-scope command should not be written: %s", op.Path)
	}
	var sawSkip bool
	for _, s := range skips {
		if s.Component == "command" && s.Name == "summarize" {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Fatalf("expected a project-scope command skip, got %+v", skips)
	}
}

// ---------------------------------------------------------------------------
// Hooks — hooks.json, unknown events skipped
// ---------------------------------------------------------------------------

func TestRender_Hooks_KnownAndUnknownEvents(t *testing.T) {
	c := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Matcher: "Bash", Type: "command", Command: "echo hi"},
		{Event: "SessionEnd", Type: "command", Command: "echo bye"}, // not a Codex event
	}}
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	ops, skips, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	// Hooks land as [hooks.*] in config.toml (Codex's single key-merge file), not
	// a separate hooks.json — so the adapter has one merge strategy.
	op := findOp(ops, "config.toml")
	if op == nil {
		t.Fatal("config.toml hooks op missing")
	}
	if op.MergeStrategy != "merge-toml-keys" {
		t.Fatalf("merge strategy = %q, want merge-toml-keys", op.MergeStrategy)
	}
	// op.Content is still JSON (the pointer-merge currency); MergeTOML emits TOML.
	var ours map[string]any
	_ = json.Unmarshal(op.Content, &ours)
	hooks := ours["hooks"].(map[string]any)
	if hooks["PreToolUse"] == nil {
		t.Fatalf("PreToolUse hook missing: %s", op.Content)
	}
	if hooks["SessionEnd"] != nil {
		t.Fatalf("unknown event SessionEnd should not be written: %s", op.Content)
	}
	var sawSkip bool
	for _, s := range skips {
		if s.Component == "hook" && s.Name == "SessionEnd" {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Fatalf("expected a skip for unknown event SessionEnd, got %+v", skips)
	}
}

// ---------------------------------------------------------------------------
// LSP — always skipped
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

	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(merged), adapter.ScopeProject, projRoot)
	if err != nil {
		t.Fatal(err)
	}

	// Codex skills live under <project>/.agents/skills/ at project scope.
	wantUserSkill := filepath.Join(projRoot, ".agents", "skills", "user-skill", "SKILL.md")
	wantProjSkill := filepath.Join(projRoot, ".agents", "skills", "proj-skill", "SKILL.md")

	var gotProjSkill bool
	for _, op := range ops {
		if op.Path == wantUserSkill {
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

func TestRender_LSP_Skipped(t *testing.T) {
	c := source.Canonical{LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}}}
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	_, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	var sawLSP bool
	for _, s := range skips {
		if s.Component == "lsp" && s.Name == "gopls" {
			sawLSP = true
		}
	}
	if !sawLSP {
		t.Fatal("expected an lsp skip")
	}
}
