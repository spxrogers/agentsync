package cursor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/cursor"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// applyRender renders c at user scope and applies the ops via a PassThroughWriter
// (no state, no backup) so the merge + version-injection logic runs against real
// files under tmp. Returns the decoded JSON at the given dest path.
func applyAndRead(t *testing.T, tmp string, c source.Canonical) func(rel string) map[string]any {
	t.Helper()
	a := cursor.New(cursor.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return func(rel string) map[string]any {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(tmp, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("parse %s: %v\n%s", rel, err, data)
		}
		return m
	}
}

// TestApply_Hooks_InjectsVersion is the regression for Cursor's required
// top-level `version` field: hooks.json must carry `version: 1` even though it is
// never part of op.Content (so it can't be stripped as an owned key).
func TestApply_Hooks_InjectsVersion(t *testing.T) {
	tmp := t.TempDir()
	c := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Matcher: "Shell", Type: "command", Command: "echo hi"},
	}}
	read := applyAndRead(t, tmp, c)
	got := read(filepath.Join(".cursor", "hooks.json"))
	if v, ok := got["version"]; !ok || jsonNum(v) != 1 {
		t.Fatalf("hooks.json must have version:1, got %v", got["version"])
	}
	hooks := got["hooks"].(map[string]any)
	if _, ok := hooks["preToolUse"]; !ok {
		t.Fatalf("preToolUse hook missing: %v", got)
	}
}

// TestApply_Hooks_PreservesForeignEvent verifies the key-merge preserves a user's
// own (Cursor-native) hook event in the same file AND keeps version present.
func TestApply_Hooks_PreservesForeignEvent(t *testing.T) {
	tmp := t.TempDir()
	hooksPath := filepath.Join(tmp, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing user hooks.json with a Cursor-native event agentsync doesn't model.
	foreign := `{
  "version": 1,
  "hooks": {
    "afterFileEdit": [ { "command": "./hooks/format.sh" } ]
  }
}`
	if err := os.WriteFile(hooksPath, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	c := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Type: "command", Command: "echo hi"},
	}}
	read := applyAndRead(t, tmp, c)
	got := read(filepath.Join(".cursor", "hooks.json"))
	hooks := got["hooks"].(map[string]any)
	if _, ok := hooks["afterFileEdit"]; !ok {
		t.Fatalf("foreign afterFileEdit event must be preserved: %v", got)
	}
	if _, ok := hooks["preToolUse"]; !ok {
		t.Fatalf("our preToolUse event must be added: %v", got)
	}
	if jsonNum(got["version"]) != 1 {
		t.Fatalf("version must remain present: %v", got["version"])
	}
}

// TestApply_Hooks_PreservesUserSetVersion pins the presence-only contract: the
// injection asserts `version` when missing but a user-set value is a foreign key
// and must survive apply verbatim, like every other foreign key.
func TestApply_Hooks_PreservesUserSetVersion(t *testing.T) {
	tmp := t.TempDir()
	hooksPath := filepath.Join(tmp, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(`{"version": 2, "hooks": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Type: "command", Command: "echo hi"},
	}}
	read := applyAndRead(t, tmp, c)
	got := read(filepath.Join(".cursor", "hooks.json"))
	if jsonNum(got["version"]) != 2 {
		t.Fatalf("user-set version must be preserved, got %v", got["version"])
	}
}

// TestApply_MCP_PreservesForeignServer verifies merge-json-keys preserves a
// hand-authored mcp.json's foreign server, and does NOT inject a version field
// (that injection is hooks-only).
func TestApply_MCP_PreservesForeignServer(t *testing.T) {
	tmp := t.TempDir()
	mcpPath := filepath.Join(tmp, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := `{ "mcpServers": { "user-server": { "command": "mine" } } }`
	if err := os.WriteFile(mcpPath, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	c := source.Canonical{MCPServers: []source.MCPServer{{ID: "ours", Server: source.MCPServerSpec{Command: "npx"}}}}
	read := applyAndRead(t, tmp, c)
	got := read(filepath.Join(".cursor", "mcp.json"))
	servers := got["mcpServers"].(map[string]any)
	if _, ok := servers["user-server"]; !ok {
		t.Fatalf("foreign server must be preserved: %v", got)
	}
	if _, ok := servers["ours"]; !ok {
		t.Fatalf("our server must be added: %v", got)
	}
	if _, hasVersion := got["version"]; hasVersion {
		t.Fatalf("mcp.json must NOT get a version field: %v", got)
	}
}

// TestApply_Reapply_Converges verifies a second apply with no source changes
// produces byte-identical files (deterministic version injection + marshaling).
func TestApply_Reapply_Converges(t *testing.T) {
	tmp := t.TempDir()
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "x", Server: source.MCPServerSpec{Type: "stdio", Command: "y"}}},
		Hooks:      []source.Hook{{Event: "Stop", Type: "command", Command: "echo bye"}},
	}
	a := cursor.New(cursor.Options{TargetRoot: tmp})
	for i := 0; i < 2; i++ {
		ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := os.ReadFile(filepath.Join(tmp, ".cursor", "hooks.json"))
	// Apply once more and compare bytes.
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	_ = a.Apply(ops, adapter.PassThroughWriter{})
	second, _ := os.ReadFile(filepath.Join(tmp, ".cursor", "hooks.json"))
	if string(first) != string(second) {
		t.Fatalf("re-apply not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// jsonNum normalizes a decoded JSON number (float64) to int for comparison.
func jsonNum(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return -1
}
