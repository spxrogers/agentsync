package marketplace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/marketplace"
)

func TestProject_StrictPluginJSON(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(cache, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"x","mcpServers":{"foo":{"command":"${CLAUDE_PLUGIN_ROOT}/run.sh"}}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "x"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.MCPServers) != 1 {
		t.Fatalf("mcp = %d, want 1", len(pr.MCPServers))
	}
	cmd := pr.MCPServers[0].Server.Command
	if !strings.HasPrefix(cmd, cache) {
		t.Fatalf("CLAUDE_PLUGIN_ROOT not resolved: %s", cmd)
	}
	if pr.MCPServers[0].ID != "foo" {
		t.Errorf("mcp id = %q, want foo", pr.MCPServers[0].ID)
	}
}

func TestProject_StrictPluginJSON_MultipleComponents(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"name": "multi",
		"mcpServers": {
			"srv-a": {"type": "stdio", "command": "${CLAUDE_PLUGIN_ROOT}/a"},
			"srv-b": {"type": "http", "url": "http://localhost:9000"}
		},
		"lspServers": {
			"lsp-x": {"command": "${CLAUDE_PLUGIN_ROOT}/lsp"}
		},
		"skills": ["skill-one", "skill-two"],
		"commands": "my-cmd",
		"agents": ["agent-alpha"]
	}`
	if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "multi"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.MCPServers) != 2 {
		t.Errorf("mcp count = %d, want 2", len(pr.MCPServers))
	}
	if len(pr.LSPServers) != 1 {
		t.Errorf("lsp count = %d, want 1", len(pr.LSPServers))
	}
	if len(pr.Skills) != 2 {
		t.Errorf("skills count = %d, want 2", len(pr.Skills))
	}
	if len(pr.Commands) != 1 {
		t.Errorf("commands count = %d, want 1", len(pr.Commands))
	}
	if len(pr.Subagents) != 1 {
		t.Errorf("agents count = %d, want 1", len(pr.Subagents))
	}

	// Verify CLAUDE_PLUGIN_ROOT substitution in LSP.
	lspCmd := pr.LSPServers[0].Spec.Command
	if !strings.HasPrefix(lspCmd, cache) {
		t.Errorf("LSP command not resolved: %s", lspCmd)
	}
}

func TestProject_NonStrict_EntryComponents(t *testing.T) {
	cache := t.TempDir()
	f := false
	entry := marketplace.PluginEntry{
		Name:   "ns",
		Strict: &f,
		MCPServers: map[string]any{
			"inline-srv": map[string]any{
				"command": "${CLAUDE_PLUGIN_ROOT}/inline",
				"args":    []any{"--port", "8080"},
			},
		},
		LSPServers: map[string]any{
			"inline-lsp": map[string]any{
				"command": "${CLAUDE_PLUGIN_ROOT}/lsp-inline",
			},
		},
	}

	pr, err := marketplace.Project(entry, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.MCPServers) != 1 {
		t.Fatalf("mcp count = %d, want 1", len(pr.MCPServers))
	}
	cmd := pr.MCPServers[0].Server.Command
	if !strings.HasPrefix(cmd, cache) {
		t.Errorf("CLAUDE_PLUGIN_ROOT not resolved in non-strict entry: %s", cmd)
	}
	if len(pr.MCPServers[0].Server.Args) != 2 {
		t.Errorf("args count = %d, want 2", len(pr.MCPServers[0].Server.Args))
	}
	if len(pr.LSPServers) != 1 {
		t.Errorf("lsp count = %d, want 1", len(pr.LSPServers))
	}
}

func TestProject_Strict_MissingPluginJSON(t *testing.T) {
	// Strict mode but no plugin.json — should return empty result, no error.
	cache := t.TempDir()
	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "x"}, cache)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pr.MCPServers)+len(pr.Skills)+len(pr.Commands)+len(pr.LSPServers) != 0 {
		t.Errorf("expected empty projection, got %+v", pr)
	}
}

func TestProject_Hooks_StringCommand(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"h","hooks":"${CLAUDE_PLUGIN_ROOT}/hook.sh"}`
	if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "h"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Hooks) != 1 {
		t.Fatalf("hooks = %d, want 1", len(pr.Hooks))
	}
	if pr.Hooks[0].Event != "PreToolUse" {
		t.Errorf("event = %q, want PreToolUse", pr.Hooks[0].Event)
	}
	if !strings.HasPrefix(pr.Hooks[0].Command, cache) {
		t.Errorf("hook command not resolved: %s", pr.Hooks[0].Command)
	}
}

func TestProject_Hooks_MapWithEvent(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"h","hooks":{"PostToolUse":{"command":"${CLAUDE_PLUGIN_ROOT}/post.sh","matcher":"Bash"}}}`
	if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "h"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Hooks) != 1 {
		t.Fatalf("hooks = %d, want 1", len(pr.Hooks))
	}
	h := pr.Hooks[0]
	if h.Event != "PostToolUse" {
		t.Errorf("event = %q", h.Event)
	}
	if h.Matcher != "Bash" {
		t.Errorf("matcher = %q", h.Matcher)
	}
	if !strings.HasPrefix(h.Command, cache) {
		t.Errorf("command not resolved: %s", h.Command)
	}
}

func TestProject_MCPSpec_FullFields(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"name": "full",
		"mcpServers": {
			"full-srv": {
				"type": "stdio",
				"command": "${CLAUDE_PLUGIN_ROOT}/server",
				"args": ["${CLAUDE_PLUGIN_ROOT}/config.json", "--verbose"],
				"env": {"PLUGIN_DIR": "${CLAUDE_PLUGIN_ROOT}"},
				"agents": ["claude", "opencode"]
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "full"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.MCPServers) != 1 {
		t.Fatalf("mcp = %d", len(pr.MCPServers))
	}
	spec := pr.MCPServers[0].Server
	if spec.Type != "stdio" {
		t.Errorf("type = %q", spec.Type)
	}
	if !strings.HasPrefix(spec.Command, cache) {
		t.Errorf("command not resolved: %s", spec.Command)
	}
	if len(spec.Args) != 2 || !strings.HasPrefix(spec.Args[0], cache) {
		t.Errorf("args not resolved: %v", spec.Args)
	}
	if v, ok := spec.Env["PLUGIN_DIR"]; !ok || !strings.HasPrefix(v, cache) {
		t.Errorf("env not resolved: %v", spec.Env)
	}
	if len(spec.Agents) != 2 {
		t.Errorf("agents = %v", spec.Agents)
	}
}
