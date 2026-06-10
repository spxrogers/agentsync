package gemini_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// TestApply_JSONC_CommentedSettings_NoClobber is the artifact-anchored
// regression for the JSONC clobber class: Gemini CLI itself reads settings.json
// as JSONC, so a user file with comments and trailing commas is a normal,
// documented state. The merge must preserve every foreign key (comments are
// stripped on first write — the documented v1 trade-off shared with
// opencode.json), never treat the unparseable-as-strict-JSON file as empty.
func TestApply_JSONC_CommentedSettings_NoClobber(t *testing.T) {
	tmp := t.TempDir()
	settingsPath := filepath.Join(tmp, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	native := `{
  // user's hand-edited Gemini settings
  "theme": "GitHub", /* inline */
  "model": "gemini-2.5-pro",
  "hooks": {
    "BeforeModel": [ { "matcher": "", "hooks": [ { "type": "command", "command": "./audit.sh" } ] } ],
  },
}`
	if err := os.WriteFile(settingsPath, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Type: "stdio", Command: "npx"}}},
	}
	a := gemini.New(gemini.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	got := readSettings(t, tmp) // asserts the on-disk result is valid strict JSON
	if got["theme"] != "GitHub" || got["model"] != "gemini-2.5-pro" {
		t.Fatalf("foreign keys clobbered by JSONC merge: %v", got)
	}
	if _, ok := got["hooks"].(map[string]any)["BeforeModel"]; !ok {
		t.Fatalf("foreign Gemini-only hook event clobbered: %v", got)
	}
	if _, ok := got["mcpServers"].(map[string]any)["github"]; !ok {
		t.Fatalf("our mcpServers section missing: %v", got)
	}
}

// TestIngest_JSONC_CommentedSettings verifies ingest tolerates a commented
// settings.json (instead of hard-failing the whole import) and that hook events
// agentsync cannot fully represent — Gemini-only events, or handlers with
// unmodeled fields like timeout — are left uncaptured with a warning, so a later
// apply never owns an array it would lossily rewrite.
func TestIngest_JSONC_CommentedSettings(t *testing.T) {
	tmp := t.TempDir()
	settingsPath := filepath.Join(tmp, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	native := `{
  // commented config
  "mcpServers": { "gh": { "command": "npx", "timeout": 30000 } },
  "hooks": {
    "BeforeTool": [ { "matcher": "", "hooks": [ { "type": "command", "command": "ok.sh" } ] } ],
    "AfterTool": [ { "matcher": "", "hooks": [ { "type": "command", "command": "slow.sh", "timeout": 5000 } ] } ],
    "BeforeModel": [ { "matcher": "", "hooks": [ { "type": "command", "command": "x.sh" } ] } ],
  },
}`
	if err := os.WriteFile(settingsPath, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	a := gemini.New(gemini.Options{TargetRoot: tmp, Stderr: &warn})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("commented settings.json must not fail ingest: %v", err)
	}
	if len(got.MCPServers) != 1 || got.MCPServers[0].ID != "gh" {
		t.Fatalf("MCP not ingested from commented settings: %+v", got.MCPServers)
	}
	if len(got.Hooks) != 1 || got.Hooks[0].Event != "PreToolUse" {
		t.Fatalf("only the fully-representable BeforeTool event should be captured, got %+v", got.Hooks)
	}
	out := warn.String()
	for _, wantMsg := range []string{
		`unmodeled fields (timeout)`,
		`"BeforeModel" has no canonical equivalent`,
	} {
		if !strings.Contains(out, wantMsg) {
			t.Errorf("missing warning %q in:\n%s", wantMsg, out)
		}
	}
}
