package windsurf_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/windsurf"
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

// ---------------------------------------------------------------------------
// User scope: MCP + global rules + global workflows all render
// ---------------------------------------------------------------------------

func TestRender_UserScope_MCPGlobalRulesAndWorkflows(t *testing.T) {
	enabled := true
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Type: "stdio", Command: "npx", Args: []string{"-y", "x"}, Env: map[string]string{"T": "v"}, Enabled: &enabled}}},
		Memory:     source.Memory{Body: "# mem\n"},
		Commands:   []source.Command{{Name: "deploy", Body: "do it\n"}},
	}
	ops, skips, err := windsurf.New(windsurf.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	op := findOp(ops, ".codeium/windsurf/mcp_config.json")
	if op == nil {
		t.Fatal("mcp_config.json op missing at user scope")
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
	// memory → the global rules file (always-on, frontmatter-less, verbatim).
	memOp := findOp(ops, filepath.Join(".codeium", "windsurf", "memories", "global_rules.md"))
	if memOp == nil {
		t.Fatalf("global_rules.md op missing at user scope: %+v", ops)
	}
	if !strings.HasPrefix(string(memOp.Content), "<!-- agentsync:managed memory-banner -->") {
		t.Fatalf("expected managed banner prefix on global rules: %q", memOp.Content)
	}
	if source.StripManagedBanner(string(memOp.Content)) != "# mem\n" {
		t.Fatalf("global rules must be the verbatim body under the managed banner: %q", memOp.Content)
	}
	// commands → global workflows.
	cmdOp := findOp(ops, filepath.Join(".codeium", "windsurf", "global_workflows", "deploy.md"))
	if cmdOp == nil {
		t.Fatalf("global_workflows/deploy.md op missing at user scope: %+v", ops)
	}
	if len(skips) != 0 {
		t.Fatalf("no skips expected at user scope, got %+v", skips)
	}
}

func TestRender_MCP_RemoteServerUrl(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "figma",
		Server: source.MCPServerSpec{Type: "http", URL: "https://mcp.figma.com/mcp", Headers: map[string]string{"API_KEY": "v"}},
	}}}
	ops, _, _ := windsurf.New(windsurf.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, "mcp_config.json")
	var ours map[string]any
	_ = json.Unmarshal(op.Content, &ours)
	srv := ours["mcpServers"].(map[string]any)["figma"].(map[string]any)
	if srv["serverUrl"] != "https://mcp.figma.com/mcp" {
		t.Fatalf("remote server must use serverUrl: %v", srv)
	}
	if _, hasCmd := srv["command"]; hasCmd {
		t.Fatalf("remote server must not have command: %v", srv)
	}
	if h := srv["headers"].(map[string]any); h["API_KEY"] != "v" {
		t.Fatalf("headers dropped: %v", srv)
	}
}

// ---------------------------------------------------------------------------
// Project scope: memory (rules) + commands (workflows) render; MCP skipped
// ---------------------------------------------------------------------------

func TestRender_ProjectScope_RulesAndWorkflows(t *testing.T) {
	proj := t.TempDir()
	projC := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Command: "npx"}}},
		Memory:     source.Memory{Body: "# Rules\n\nBe concise.\n"},
		Commands:   []source.Command{{Name: "deploy", Frontmatter: map[string]any{"description": "Deploy", "argument-hint": "<env>"}, Body: "Run deploy.\n"}},
	}
	c := projC
	c.Project = &projC
	ops, skips, err := windsurf.New(windsurf.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	// Memory → .windsurf/rules/agentsync.md with the documented activation
	// frontmatter (workspace rules declare their trigger in frontmatter).
	memOp := findOp(ops, ".windsurf/rules/agentsync.md")
	if memOp == nil {
		t.Fatal("rules/agentsync.md op missing at project scope")
	}
	// The activation frontmatter must stay at byte 0; the managed banner sits
	// after it and strips back out to leave frontmatter + verbatim body.
	if !strings.HasPrefix(string(memOp.Content), "---\ntrigger: always_on\n---\n") {
		t.Fatalf("activation frontmatter must stay at byte 0: %q", memOp.Content)
	}
	if !strings.Contains(string(memOp.Content), "<!-- agentsync:managed memory-banner -->") {
		t.Fatalf("expected managed banner after the frontmatter: %q", memOp.Content)
	}
	if source.StripManagedBanner(string(memOp.Content)) != "---\ntrigger: always_on\n---\n\n# Rules\n\nBe concise.\n" {
		t.Fatalf("memory rule must carry trigger: always_on frontmatter under the managed banner: %q", memOp.Content)
	}
	// Command → .windsurf/workflows/deploy.md (plain body; frontmatter dropped).
	cmdOp := findOp(ops, ".windsurf/workflows/deploy.md")
	if cmdOp == nil {
		t.Fatal("workflows/deploy.md op missing")
	}
	if string(cmdOp.Content) != "Run deploy.\n" {
		t.Fatalf("workflow should be plain body: %q", cmdOp.Content)
	}
	if !hasSkip(skips, "command-frontmatter", "deploy") {
		t.Errorf("expected command-frontmatter skip, got %+v", skips)
	}
	// MCP has no project target → reported skip, no op.
	if findOp(ops, "mcp_config.json") != nil {
		t.Fatalf("MCP must not render at project scope: %+v", ops)
	}
	if !hasSkip(skips, "mcp", "github") {
		t.Errorf("expected project-scope MCP skip, got %+v", skips)
	}
}

// ---------------------------------------------------------------------------
// Unsupported components always skipped
// ---------------------------------------------------------------------------

func TestRender_UnsupportedComponentsSkipped(t *testing.T) {
	c := source.Canonical{
		Skills:     []source.Skill{{Name: "demo", Frontmatter: map[string]any{"name": "demo"}, Body: "x\n"}},
		Subagents:  []source.Subagent{{Name: "rev", Body: "y\n"}},
		Hooks:      []source.Hook{{Event: "PreToolUse", Type: "command", Command: "echo"}},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
	}
	_, skips, err := windsurf.New(windsurf.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct{ comp, name string }{{"skill", "demo"}, {"subagent", "rev"}, {"hook", "PreToolUse"}, {"lsp", "gopls"}} {
		if !hasSkip(skips, w.comp, w.name) {
			t.Errorf("expected %s skip for %q, got %+v", w.comp, w.name, skips)
		}
	}
}
