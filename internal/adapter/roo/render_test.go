package roo_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/roo"
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

// projOf returns a canonical with the same content set as the project overlay,
// so project-scope Render (which renders c.Project) sees it.
func projOf(c source.Canonical) source.Canonical {
	c.Project = &source.Canonical{
		MCPServers: c.MCPServers,
		Memory:     c.Memory,
		Commands:   c.Commands,
	}
	return c
}

// ---------------------------------------------------------------------------
// Project scope: MCP (.roo/mcp.json) + rules + commands
// ---------------------------------------------------------------------------

func TestRender_ProjectScope_All(t *testing.T) {
	proj := t.TempDir()
	enabled := true
	c := projOf(source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Type: "stdio", Command: "npx", Args: []string{"-y", "x"}, Env: map[string]string{"T": "v"}, Enabled: &enabled}}},
		Memory:     source.Memory{Body: "# Rules\n\nBe concise.\n"},
		Commands:   []source.Command{{Name: "review", Frontmatter: map[string]any{"description": "Review", "argument-hint": "<file>", "allowed-tools": "Read"}, Body: "Review it.\n"}},
	})
	ops, skips, err := roo.New(roo.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	// MCP → .roo/mcp.json (merge-json-keys)
	mcpOp := findOp(ops, ".roo/mcp.json")
	if mcpOp == nil {
		t.Fatal("mcp.json op missing at project scope")
	}
	if mcpOp.MergeStrategy != "merge-json-keys" {
		t.Fatalf("merge strategy = %q, want merge-json-keys", mcpOp.MergeStrategy)
	}
	var ours map[string]any
	_ = json.Unmarshal(mcpOp.Content, &ours)
	srv := ours["mcpServers"].(map[string]any)["github"].(map[string]any)
	if srv["command"] != "npx" || srv["env"].(map[string]any)["T"] != "v" {
		t.Fatalf("stdio server malformed: %v", srv)
	}
	// Memory → .roo/rules/agentsync.md (plain body)
	memOp := findOp(ops, ".roo/rules/agentsync.md")
	if memOp == nil || source.StripManagedBanner(string(memOp.Content)) != "# Rules\n\nBe concise.\n" {
		t.Fatalf("memory rule wrong (under managed banner): %+v", memOp)
	}
	// Command → .roo/commands/review.md; description + argument-hint KEPT; allowed-tools dropped
	cmdOp := findOp(ops, ".roo/commands/review.md")
	if cmdOp == nil {
		t.Fatal("command op missing")
	}
	content := string(cmdOp.Content)
	if !strings.Contains(content, "description") || !strings.Contains(content, "argument-hint") || !strings.Contains(content, "<file>") {
		t.Fatalf("Roo should keep description + argument-hint: %s", content)
	}
	if strings.Contains(content, "allowed-tools") {
		t.Fatalf("allowed-tools must be dropped: %s", content)
	}
	if !hasSkip(skips, "command-frontmatter", "review") {
		t.Errorf("expected command-frontmatter skip listing allowed-tools, got %+v", skips)
	}
}

func TestRender_MCP_RemoteType(t *testing.T) {
	proj := t.TempDir()
	c := projOf(source.Canonical{MCPServers: []source.MCPServer{
		{ID: "fig", Server: source.MCPServerSpec{Type: "http", URL: "https://x/mcp", Headers: map[string]string{"A": "b"}}},
		{ID: "sse", Server: source.MCPServerSpec{Type: "sse", URL: "https://x/sse"}},
	}})
	ops, _, _ := roo.New(roo.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	op := findOp(ops, ".roo/mcp.json")
	var ours map[string]any
	_ = json.Unmarshal(op.Content, &ours)
	servers := ours["mcpServers"].(map[string]any)
	if servers["fig"].(map[string]any)["type"] != "streamable-http" {
		t.Fatalf("http should map to streamable-http: %v", servers["fig"])
	}
	if servers["sse"].(map[string]any)["type"] != "sse" {
		t.Fatalf("sse should map to sse: %v", servers["sse"])
	}
}

// ---------------------------------------------------------------------------
// User scope: rules + commands render; MCP skipped (globalStorage)
// ---------------------------------------------------------------------------

func TestRender_UserScope_NoMCP(t *testing.T) {
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Command: "npx"}}},
		Memory:     source.Memory{Body: "mem\n"},
		Commands:   []source.Command{{Name: "deploy", Body: "do it\n"}},
	}
	tmp := t.TempDir()
	ops, skips, err := roo.New(roo.Options{TargetRoot: tmp}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	// rules + commands land under ~/.roo
	if op := findOp(ops, ".roo/rules/agentsync.md"); op == nil || op.Path != filepath.Join(tmp, ".roo", "rules", "agentsync.md") {
		t.Fatalf("user-scope memory rule wrong: %+v", ops)
	}
	if findOp(ops, ".roo/commands/deploy.md") == nil {
		t.Fatal("user-scope command missing")
	}
	// MCP skipped (no project file at user scope)
	if findOp(ops, "mcp.json") != nil {
		t.Fatalf("MCP must not render at user scope: %+v", ops)
	}
	if !hasSkip(skips, "mcp", "github") {
		t.Errorf("expected user-scope MCP skip, got %+v", skips)
	}
}

func TestRender_UnsupportedComponentsSkipped(t *testing.T) {
	c := source.Canonical{
		Skills:     []source.Skill{{Name: "demo", Frontmatter: map[string]any{"name": "demo"}, Body: "x\n"}},
		Subagents:  []source.Subagent{{Name: "rev", Body: "y\n"}},
		Hooks:      []source.Hook{{Event: "PreToolUse", Type: "command", Command: "echo"}},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
	}
	_, skips, err := roo.New(roo.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct{ comp, name string }{{"skill", "demo"}, {"subagent", "rev"}, {"hook", "PreToolUse"}, {"lsp", "gopls"}} {
		if !hasSkip(skips, w.comp, w.name) {
			t.Errorf("expected %s skip for %q, got %+v", w.comp, w.name, skips)
		}
	}
}
