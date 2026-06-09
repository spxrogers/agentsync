package gemini_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func readSettings(t *testing.T, tmp string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tmp, ".gemini", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse settings.json: %v\n%s", err, data)
	}
	return m
}

// TestApply_MCPAndHooks_ShareSettingsJSON verifies the two sections agentsync
// owns (mcpServers + hooks) both land in the SAME settings.json without
// clobbering each other (two merge-json-keys ops to one file).
func TestApply_MCPAndHooks_ShareSettingsJSON(t *testing.T) {
	tmp := t.TempDir()
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Type: "stdio", Command: "npx"}}},
		Hooks:      []source.Hook{{Event: "PreToolUse", Type: "command", Command: "echo hi"}},
	}
	a := gemini.New(gemini.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	got := readSettings(t, tmp)
	if _, ok := got["mcpServers"].(map[string]any)["github"]; !ok {
		t.Fatalf("mcpServers.github missing from settings.json: %v", got)
	}
	if _, ok := got["hooks"].(map[string]any)["BeforeTool"]; !ok {
		t.Fatalf("hooks.BeforeTool missing from settings.json: %v", got)
	}
}

// TestApply_PreservesForeignSettings verifies merge-json-keys preserves a user's
// foreign top-level key and foreign MCP server in settings.json.
func TestApply_PreservesForeignSettings(t *testing.T) {
	tmp := t.TempDir()
	settingsPath := filepath.Join(tmp, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := `{
  "theme": "GitHub",
  "mcpServers": { "user-server": { "command": "mine" } }
}`
	if err := os.WriteFile(settingsPath, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	c := source.Canonical{MCPServers: []source.MCPServer{{ID: "ours", Server: source.MCPServerSpec{Command: "npx"}}}}
	a := gemini.New(gemini.Options{TargetRoot: tmp})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	got := readSettings(t, tmp)
	if got["theme"] != "GitHub" {
		t.Fatalf("foreign top-level key clobbered: %v", got)
	}
	servers := got["mcpServers"].(map[string]any)
	if _, ok := servers["user-server"]; !ok {
		t.Fatalf("foreign server clobbered: %v", servers)
	}
	if _, ok := servers["ours"]; !ok {
		t.Fatalf("our server not added: %v", servers)
	}
}

// TestApply_Reapply_Converges verifies a second apply with no source changes
// produces byte-identical settings.json (deterministic marshaling).
func TestApply_Reapply_Converges(t *testing.T) {
	tmp := t.TempDir()
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "x", Server: source.MCPServerSpec{Type: "stdio", Command: "y"}}},
		Hooks:      []source.Hook{{Event: "SessionStart", Type: "command", Command: "echo go"}},
	}
	a := gemini.New(gemini.Options{TargetRoot: tmp})
	apply := func() []byte {
		ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
		if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(tmp, ".gemini", "settings.json"))
		return data
	}
	first := apply()
	second := apply()
	if string(first) != string(second) {
		t.Fatalf("re-apply not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}
