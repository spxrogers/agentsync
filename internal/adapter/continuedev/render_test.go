package continuedev_test

import (
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/continuedev"
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

// parseBlock decodes a rendered Continue MCP YAML block and returns its single
// server entry.
func parseServer(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var block map[string]any
	if err := yaml.Unmarshal(content, &block); err != nil {
		t.Fatalf("block not valid YAML: %v\n%s", err, content)
	}
	servers, ok := block["mcpServers"].([]any)
	if !ok || len(servers) != 1 {
		t.Fatalf("block mcpServers malformed: %v", block)
	}
	return servers[0].(map[string]any)
}

// ---------------------------------------------------------------------------
// MCP — one YAML block file per server
// ---------------------------------------------------------------------------

func TestRender_MCP_StdioBlock(t *testing.T) {
	enabled := true
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "github",
		Server: source.MCPServerSpec{
			Type: "stdio", Command: "npx", Args: []string{"-y", "x"},
			Env: map[string]string{"TOKEN": "abc"}, Agents: []string{"continue"}, Enabled: &enabled,
		},
	}}}
	ops, _, err := continuedev.New(continuedev.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	op := findOp(ops, ".continue/mcpServers/github.yaml")
	if op == nil {
		t.Fatal("github.yaml block op missing")
	}
	if op.MergeStrategy != "replace" {
		t.Fatalf("MCP blocks are whole-file replace, got %q", op.MergeStrategy)
	}
	srv := parseServer(t, op.Content)
	if srv["name"] != "github" || srv["type"] != "stdio" || srv["command"] != "npx" {
		t.Fatalf("stdio block malformed: %v", srv)
	}
	if env := srv["env"].(map[string]any); env["TOKEN"] != "abc" {
		t.Fatalf("env dropped: %v", srv)
	}
}

func TestRender_MCP_RemoteHeaders(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "figma",
		Server: source.MCPServerSpec{Type: "http", URL: "https://mcp.figma.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"}},
	}}}
	ops, _, _ := continuedev.New(continuedev.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".continue/mcpServers/figma.yaml")
	if op == nil {
		t.Fatal("figma.yaml block missing")
	}
	srv := parseServer(t, op.Content)
	if srv["type"] != "streamable-http" {
		t.Fatalf("http should map to streamable-http: %v", srv)
	}
	if srv["url"] != "https://mcp.figma.com/mcp" {
		t.Fatalf("url missing: %v", srv)
	}
	ro, ok := srv["requestOptions"].(map[string]any)
	if !ok {
		t.Fatalf("requestOptions missing: %v", srv)
	}
	if h := ro["headers"].(map[string]any); h["Authorization"] != "Bearer t" {
		t.Fatalf("headers dropped: %v", ro)
	}
}

func TestRender_MCP_SkipsDisabledAndOtherAgents(t *testing.T) {
	disabled := false
	c := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "off", Server: source.MCPServerSpec{Command: "x", Enabled: &disabled}},
		{ID: "claude-only", Server: source.MCPServerSpec{Command: "x", Agents: []string{"claude"}}},
	}}
	ops, _, _ := continuedev.New(continuedev.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if op := findOp(ops, "mcpServers/"); op != nil {
		t.Fatalf("should emit no block for disabled/other-agent servers: %s", op.Path)
	}
}

// ---------------------------------------------------------------------------
// Memory — a plain (no-frontmatter) always-apply rule
// ---------------------------------------------------------------------------

func TestRender_Memory_PlainRule(t *testing.T) {
	c := source.Canonical{Memory: source.Memory{Body: "# Rules\n\nBe concise.\n"}}
	ops, _, _ := continuedev.New(continuedev.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".continue/rules/agentsync.md")
	if op == nil {
		t.Fatal("agentsync.md rule op missing")
	}
	if !strings.HasPrefix(string(op.Content), "<!-- agentsync:managed memory-banner -->") {
		t.Fatalf("expected managed banner prefix: %q", op.Content)
	}
	if source.StripManagedBanner(string(op.Content)) != "# Rules\n\nBe concise.\n" {
		t.Fatalf("memory body should be verbatim under the managed banner (no frontmatter): %q", op.Content)
	}
}

// ---------------------------------------------------------------------------
// Command — prompt block; argument-hint dropped + reported
// ---------------------------------------------------------------------------

func TestRender_Command_PromptBlock(t *testing.T) {
	c := source.Canonical{Commands: []source.Command{{
		Name:        "summarize",
		Frontmatter: map[string]any{"description": "Summarize code", "argument-hint": "<file>"},
		Body:        "Summarize the file.\n",
	}}}
	ops, skips, _ := continuedev.New(continuedev.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".continue/prompts/summarize.md")
	if op == nil {
		t.Fatal("summarize.md prompt op missing")
	}
	content := string(op.Content)
	if !strings.Contains(content, "invokable: true") || !strings.Contains(content, "name: summarize") {
		t.Fatalf("prompt frontmatter missing name/invokable: %s", content)
	}
	if !strings.Contains(content, "Summarize the file") {
		t.Fatalf("prompt body missing: %s", content)
	}
	if strings.Contains(content, "argument-hint") {
		t.Fatalf("argument-hint must be dropped: %s", content)
	}
	if !hasSkip(skips, "command-frontmatter", "summarize") {
		t.Fatalf("expected a command-frontmatter skip, got %+v", skips)
	}
}

// ---------------------------------------------------------------------------
// Skips — skill/subagent/hook/lsp have no Continue target
// ---------------------------------------------------------------------------

func TestRender_UnsupportedComponentsSkipped(t *testing.T) {
	c := source.Canonical{
		Skills:     []source.Skill{{Name: "demo", Frontmatter: map[string]any{"name": "demo"}, Body: "x\n"}},
		Subagents:  []source.Subagent{{Name: "rev", Body: "y\n"}},
		Hooks:      []source.Hook{{Event: "PreToolUse", Type: "command", Command: "echo"}},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
	}
	ops, skips, err := continuedev.New(continuedev.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("no ops expected for unsupported-only canonical, got %+v", ops)
	}
	for _, want := range []struct{ comp, name string }{
		{"skill", "demo"}, {"subagent", "rev"}, {"hook", "PreToolUse"}, {"lsp", "gopls"},
	} {
		if !hasSkip(skips, want.comp, want.name) {
			t.Errorf("expected %s skip for %q, got %+v", want.comp, want.name, skips)
		}
	}
}
