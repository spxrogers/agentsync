package cline_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/cline"
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

func hasSkip(skips []adapter.Skip, component, name string) bool {
	for _, s := range skips {
		if s.Component == component && s.Name == name {
			return true
		}
	}
	return false
}

func projOf(c source.Canonical) source.Canonical {
	c.Project = &source.Canonical{MCPServers: c.MCPServers, Memory: c.Memory, Commands: c.Commands}
	return c
}

// ---------------------------------------------------------------------------
// User scope: MCP (~/.cline/mcp.json) renders; memory + commands skipped
// ---------------------------------------------------------------------------

func TestRender_UserScope_MCPOnly(t *testing.T) {
	enabled := true
	tmp := t.TempDir()
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Type: "stdio", Command: "npx", Args: []string{"-y", "x"}, Env: map[string]string{"T": "v"}, Enabled: &enabled}}},
		Memory:     source.Memory{Body: "# mem\n"},
		Commands:   []source.Command{{Name: "deploy", Body: "do it\n"}},
	}
	ops, skips, err := cline.New(cline.Options{TargetRoot: tmp}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	op := findOp(ops, ".cline/mcp.json")
	if op == nil || op.Path != filepath.Join(tmp, ".cline", "mcp.json") {
		t.Fatalf("mcp.json op missing/wrong at user scope: %+v", ops)
	}
	if op.MergeStrategy != "merge-json-keys" {
		t.Fatalf("merge strategy = %q, want merge-json-keys", op.MergeStrategy)
	}
	var ours map[string]any
	_ = json.Unmarshal(op.Content, &ours)
	srv := ours["mcpServers"].(map[string]any)["github"].(map[string]any)
	if srv["command"] != "npx" || srv["env"].(map[string]any)["T"] != "v" {
		t.Fatalf("stdio server malformed: %v", srv)
	}
	if _, hasType := srv["type"]; hasType {
		t.Fatalf("Cline infers transport; must not write a type key: %v", srv)
	}
	if !hasSkip(skips, "memory", "rules") || !hasSkip(skips, "command", "deploy") {
		t.Errorf("expected user-scope memory + command skips, got %+v", skips)
	}
	if findOp(ops, "agentsync.md") != nil || findOp(ops, "deploy.md") != nil {
		t.Fatalf("memory/commands must not render at user scope: %+v", ops)
	}
}

func TestRender_MCP_RemoteUrl(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "figma",
		Server: source.MCPServerSpec{Type: "http", URL: "https://mcp.figma.com/mcp", Headers: map[string]string{"API_KEY": "v"}},
	}}}
	ops, _, _ := cline.New(cline.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, "mcp.json")
	var ours map[string]any
	_ = json.Unmarshal(op.Content, &ours)
	srv := ours["mcpServers"].(map[string]any)["figma"].(map[string]any)
	if srv["url"] != "https://mcp.figma.com/mcp" {
		t.Fatalf("remote server must use url: %v", srv)
	}
	if _, hasCmd := srv["command"]; hasCmd {
		t.Fatalf("remote server must not have command: %v", srv)
	}
	if h := srv["headers"].(map[string]any); h["API_KEY"] != "v" {
		t.Fatalf("headers dropped: %v", srv)
	}
}

// ---------------------------------------------------------------------------
// Project scope: memory (.clinerules/) + workflows; MCP skipped
// ---------------------------------------------------------------------------

func TestRender_ProjectScope_RulesAndWorkflows(t *testing.T) {
	proj := t.TempDir()
	c := projOf(source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Command: "npx"}}},
		Memory:     source.Memory{Body: "# Rules\n\nBe concise.\n"},
		Commands:   []source.Command{{Name: "deploy", Frontmatter: map[string]any{"description": "Deploy"}, Body: "Run deploy.\n"}},
	})
	ops, skips, err := cline.New(cline.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	memOp := findOp(ops, ".clinerules/agentsync.md")
	if memOp == nil {
		t.Fatal("memory rule op missing")
	}
	if !strings.HasPrefix(string(memOp.Content), "<!-- agentsync:managed -->") {
		t.Fatalf("expected managed banner prefix: %q", memOp.Content)
	}
	if source.StripManagedBanner(string(memOp.Content)) != "# Rules\n\nBe concise.\n" {
		t.Fatalf("memory rule wrong (under managed banner): %+v", memOp)
	}
	cmdOp := findOp(ops, ".clinerules/workflows/deploy.md")
	if cmdOp == nil || string(cmdOp.Content) != "Run deploy.\n" {
		t.Fatalf("workflow should be plain body: %+v", cmdOp)
	}
	if !hasSkip(skips, "command-frontmatter", "deploy") {
		t.Errorf("expected command-frontmatter skip, got %+v", skips)
	}
	if findOp(ops, "mcp.json") != nil {
		t.Fatalf("MCP must not render at project scope: %+v", ops)
	}
	if !hasSkip(skips, "mcp", "github") {
		t.Errorf("expected project-scope MCP skip, got %+v", skips)
	}
}

func TestRender_UnsupportedComponentsSkipped(t *testing.T) {
	c := source.Canonical{
		Skills:     []source.Skill{{Name: "demo", Frontmatter: map[string]any{"name": "demo"}, Body: "x\n"}},
		Subagents:  []source.Subagent{{Name: "rev", Body: "y\n"}},
		Hooks:      []source.Hook{{Event: "PreToolUse", Type: "command", Command: "echo"}},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
	}
	_, skips, err := cline.New(cline.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct{ comp, name string }{{"skill", "demo"}, {"subagent", "rev"}, {"hook", "PreToolUse"}, {"lsp", "gopls"}} {
		if !hasSkip(skips, w.comp, w.name) {
			t.Errorf("expected %s skip for %q, got %+v", w.comp, w.name, skips)
		}
	}
}
