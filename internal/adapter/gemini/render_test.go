package gemini_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func findOp(ops []adapter.FileOp, suffix string) *adapter.FileOp {
	for i := range ops {
		if strings.HasSuffix(filepath.ToSlash(ops[i].Path), suffix) {
			return &ops[i]
		}
	}
	return nil
}

func hasSkip(skips []adapter.Skip, component, name string, kind adapter.SkipKind) *adapter.Skip {
	for i := range skips {
		if skips[i].Component == component && skips[i].Name == name && skips[i].Kind == kind {
			return &skips[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// MCP — settings.json mcpServers; stdio + SSE(url) + HTTP(httpUrl)
// ---------------------------------------------------------------------------

func TestRender_MCP_Stdio(t *testing.T) {
	enabled := true
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "github",
		Server: source.MCPServerSpec{
			Type: "stdio", Command: "npx", Args: []string{"-y", "x"},
			Env: map[string]string{"TOKEN": "abc"}, Agents: []string{"gemini"}, Enabled: &enabled,
		},
	}}}
	ops, _, err := gemini.New(gemini.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	op := findOp(ops, ".gemini/settings.json")
	if op == nil {
		t.Fatal("settings.json op missing")
	}
	if op.MergeStrategy != "merge-jsonc-keys" {
		t.Fatalf("merge strategy = %q, want merge-jsonc-keys", op.MergeStrategy)
	}
	var ours map[string]any
	if err := json.Unmarshal(op.Content, &ours); err != nil {
		t.Fatalf("op.Content not JSON: %v", err)
	}
	srv := ours["mcpServers"].(map[string]any)["github"].(map[string]any)
	if srv["command"] != "npx" {
		t.Fatalf("command missing: %v", srv)
	}
	if env := srv["env"].(map[string]any); env["TOKEN"] != "abc" {
		t.Fatalf("env dropped: %v", srv)
	}
	if _, hasType := srv["type"]; hasType {
		t.Fatalf("gemini stdio server must not carry a `type` key: %v", srv)
	}
}

func TestRender_MCP_HTTPUsesHttpUrl(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "figma",
		Server: source.MCPServerSpec{Type: "http", URL: "https://mcp.figma.com/mcp", Headers: map[string]string{"X-Region": "us"}},
	}}}
	ops, _, _ := gemini.New(gemini.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".gemini/settings.json")
	var ours map[string]any
	_ = json.Unmarshal(op.Content, &ours)
	srv := ours["mcpServers"].(map[string]any)["figma"].(map[string]any)
	if srv["httpUrl"] != "https://mcp.figma.com/mcp" {
		t.Fatalf("HTTP server must use httpUrl: %v", srv)
	}
	if _, hasURL := srv["url"]; hasURL {
		t.Fatalf("HTTP server must not also set url: %v", srv)
	}
	if h := srv["headers"].(map[string]any); h["X-Region"] != "us" {
		t.Fatalf("headers dropped: %v", srv)
	}
}

func TestRender_MCP_SSEUsesUrl(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "sse-srv",
		Server: source.MCPServerSpec{Type: "sse", URL: "https://example/sse"},
	}}}
	ops, _, _ := gemini.New(gemini.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".gemini/settings.json")
	var ours map[string]any
	_ = json.Unmarshal(op.Content, &ours)
	srv := ours["mcpServers"].(map[string]any)["sse-srv"].(map[string]any)
	if srv["url"] != "https://example/sse" {
		t.Fatalf("SSE server must use url: %v", srv)
	}
	if _, hasHTTP := srv["httpUrl"]; hasHTTP {
		t.Fatalf("SSE server must not set httpUrl: %v", srv)
	}
}

func TestRender_MCP_SkipsDisabledAndOtherAgents(t *testing.T) {
	disabled := false
	c := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "off", Server: source.MCPServerSpec{Command: "x", Enabled: &disabled}},
		{ID: "claude-only", Server: source.MCPServerSpec{Command: "x", Agents: []string{"claude"}}},
	}}
	ops, _, _ := gemini.New(gemini.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	// settings.json may still be absent (no servers, no hooks).
	if op := findOp(ops, "settings.json"); op != nil {
		var ours map[string]any
		_ = json.Unmarshal(op.Content, &ours)
		if _, ok := ours["mcpServers"]; ok {
			t.Fatalf("should emit no mcpServers for disabled/other-agent servers: %s", op.Content)
		}
	}
}

// ---------------------------------------------------------------------------
// Memory — GEMINI.md (user: ~/.gemini/GEMINI.md, project: repo-root GEMINI.md)
// ---------------------------------------------------------------------------

func TestRender_Memory_UserScope(t *testing.T) {
	tmp := t.TempDir()
	c := source.Canonical{Memory: source.Memory{Body: "# Mem\n\nBe helpful.\n"}}
	ops, _, _ := gemini.New(gemini.Options{TargetRoot: tmp}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, "GEMINI.md")
	if op == nil {
		t.Fatal("GEMINI.md op missing")
	}
	if op.Path != filepath.Join(tmp, ".gemini", "GEMINI.md") {
		t.Fatalf("user memory should be ~/.gemini/GEMINI.md: %s", op.Path)
	}
	if !strings.Contains(string(op.Content), "Be helpful") {
		t.Fatalf("memory body missing: %s", op.Content)
	}
}

func TestRender_Memory_ProjectScope_RepoRoot(t *testing.T) {
	proj := t.TempDir()
	c := source.Canonical{
		Memory:  source.Memory{Body: "x\n"},
		Project: &source.Canonical{Memory: source.Memory{Body: "x\n"}},
	}
	ops, _, _ := gemini.New(gemini.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	op := findOp(ops, "GEMINI.md")
	if op == nil || op.Path != filepath.Join(proj, "GEMINI.md") {
		t.Fatalf("project memory should be repo-root GEMINI.md; ops=%+v", ops)
	}
}

// ---------------------------------------------------------------------------
// Command — TOML {description, prompt}; extra frontmatter dropped + reported
// ---------------------------------------------------------------------------

func TestRender_Command_TOML(t *testing.T) {
	c := source.Canonical{Commands: []source.Command{{
		Name:        "summarize",
		Frontmatter: map[string]any{"description": "Summarize code", "argument-hint": "<file>"},
		Body:        "Summarize {{args}}.\n",
	}}}
	ops, skips, _ := gemini.New(gemini.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".gemini/commands/summarize.toml")
	if op == nil {
		t.Fatal("summarize.toml op missing")
	}
	var cf struct {
		Description string `toml:"description"`
		Prompt      string `toml:"prompt"`
	}
	if err := toml.Unmarshal(op.Content, &cf); err != nil {
		t.Fatalf("command not valid TOML: %v\n%s", err, op.Content)
	}
	if cf.Description != "Summarize code" || cf.Prompt != "Summarize {{args}}.\n" {
		t.Fatalf("command TOML wrong: %+v", cf)
	}
	sk := hasSkip(skips, "command", "summarize", adapter.SkipReduced)
	if sk == nil || !strings.Contains(sk.Reason, "argument-hint") {
		t.Fatalf("expected reduced command skip listing argument-hint, got %+v", skips)
	}
}

// ---------------------------------------------------------------------------
// Subagent — markdown; tools/color dropped + reported; name defaulted
// ---------------------------------------------------------------------------

func TestRender_Subagent_DropsToolsKeepsCore(t *testing.T) {
	c := source.Canonical{Subagents: []source.Subagent{{
		Name: "review",
		Frontmatter: map[string]any{
			"description": "Code review", "model": "gemini-3-flash",
			"tools": []any{"Read", "Grep"}, "color": "blue",
		},
		Body: "Review the code.\n",
	}}}
	ops, skips, _ := gemini.New(gemini.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".gemini/agents/review.md")
	if op == nil {
		t.Fatal("review.md op missing")
	}
	content := string(op.Content)
	if !strings.Contains(content, "Review the code") || !strings.Contains(content, "Code review") {
		t.Fatalf("body/description missing: %s", content)
	}
	if !strings.Contains(content, "name: review") {
		t.Fatalf("name should be set (Gemini requires it): %s", content)
	}
	if strings.Contains(content, "tools") || strings.Contains(content, "color") {
		t.Fatalf("tools/color must be dropped: %s", content)
	}
	sk := hasSkip(skips, "subagent", "review", adapter.SkipReduced)
	if sk == nil || !strings.Contains(sk.Reason, "tools") || !strings.Contains(sk.Reason, "color") {
		t.Fatalf("expected reduced subagent skip listing tools+color, got %+v", skips)
	}
}

// ---------------------------------------------------------------------------
// Hooks — settings.json "hooks", nested shape, events remapped, unknown skipped
// ---------------------------------------------------------------------------

func TestRender_Hooks_MapsEventsNestedShape(t *testing.T) {
	c := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Matcher: "write_file", Type: "command", Command: "echo hi"},
		{Event: "Stop", Type: "command", Command: "echo done"},
		{Event: "SubagentStop", Type: "command", Command: "echo nope"}, // no Gemini equivalent
	}}
	ops, skips, _ := gemini.New(gemini.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".gemini/settings.json")
	if op == nil {
		t.Fatal("settings.json hooks op missing")
	}
	var ours map[string]any
	_ = json.Unmarshal(op.Content, &ours)
	hooks := ours["hooks"].(map[string]any)
	bt, ok := hooks["BeforeTool"].([]any)
	if !ok || len(bt) != 1 {
		t.Fatalf("PreToolUse should map to BeforeTool: %s", op.Content)
	}
	entry := bt[0].(map[string]any)
	if entry["matcher"] != "write_file" {
		t.Fatalf("matcher lost: %v", entry)
	}
	nested, ok := entry["hooks"].([]any)
	if !ok || len(nested) != 1 || nested[0].(map[string]any)["command"] != "echo hi" {
		t.Fatalf("nested hooks array shape wrong: %v", entry)
	}
	if _, ok := hooks["AfterAgent"]; !ok {
		t.Fatalf("Stop should map to AfterAgent: %s", op.Content)
	}
	if hasSkip(skips, "hook", "SubagentStop", adapter.SkipDropped) == nil {
		t.Fatalf("expected a skip for unmapped event SubagentStop, got %+v", skips)
	}
}

// ---------------------------------------------------------------------------
// Skill + LSP — always skipped (no Gemini concept)
// ---------------------------------------------------------------------------

func TestRender_SkillAndLSP_Skipped(t *testing.T) {
	c := source.Canonical{
		Skills:     []source.Skill{{Name: "demo", Frontmatter: map[string]any{"name": "demo"}, Body: "x\n"}},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
	}
	ops, skips, err := gemini.New(gemini.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if op := findOp(ops, "SKILL.md"); op != nil {
		t.Fatalf("Gemini has no skills; none should be written: %s", op.Path)
	}
	if hasSkip(skips, "skill", "demo", adapter.SkipDropped) == nil {
		t.Fatalf("expected a skill skip, got %+v", skips)
	}
	if hasSkip(skips, "lsp", "gopls", adapter.SkipDropped) == nil {
		t.Fatalf("expected an lsp skip, got %+v", skips)
	}
}
