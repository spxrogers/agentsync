package codex_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter/codex"
)

// TestMergeTOML_PreservesForeignKeys is the core invariant: agentsync owns
// [mcp_servers.*] but must not clobber the user's other config.toml keys
// (model, approval_policy, [plugins.*], …) when it writes MCP servers.
func TestMergeTOML_PreservesForeignKeys(t *testing.T) {
	existing := []byte(`model = "gpt-5.5"
approval_policy = "on-request"

[mcp_servers.old]
command = "old-cmd"

[plugins."gmail@openai-curated"]
enabled = false
`)
	ours := map[string]any{
		"mcp_servers": map[string]any{
			"github": map[string]any{
				"command": "npx",
				"args":    []any{"-y", "x"},
			},
		},
	}
	out, err := codex.MergeTOML(existing, ours, nil)
	if err != nil {
		t.Fatalf("MergeTOML: %v", err)
	}
	var got map[string]any
	if err := toml.Unmarshal(out, &got); err != nil {
		t.Fatalf("re-parse merged TOML: %v\n%s", err, out)
	}
	if got["model"] != "gpt-5.5" {
		t.Fatalf("foreign key 'model' lost: %v", got["model"])
	}
	if got["approval_policy"] != "on-request" {
		t.Fatalf("foreign key 'approval_policy' lost: %v", got["approval_policy"])
	}
	plugins, ok := got["plugins"].(map[string]any)
	if !ok || plugins["gmail@openai-curated"] == nil {
		t.Fatalf("foreign [plugins.*] table lost: %v", got["plugins"])
	}
	servers, ok := got["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_servers missing: %v", got)
	}
	// Our server is added; the user's foreign sibling server is preserved.
	if servers["github"] == nil {
		t.Fatalf("our mcp_servers.github not written: %v", servers)
	}
	if servers["old"] == nil {
		t.Fatalf("foreign sibling mcp_servers.old lost: %v", servers)
	}
}

// TestMergeTOML_RemovesOrphanedOwnedKeys proves that an owned pointer absent
// from `ours` is deleted (the orphan-cleanup path), while foreign keys stay.
func TestMergeTOML_RemovesOrphanedOwnedKeys(t *testing.T) {
	existing := []byte(`model = "gpt-5.5"

[mcp_servers.github]
command = "npx"
`)
	// ours no longer contains mcp_servers.github; the owned pointer drives removal.
	ours := map[string]any{"mcp_servers": map[string]any{}}
	out, err := codex.MergeTOML(existing, ours, []string{"/mcp_servers/github"})
	if err != nil {
		t.Fatalf("MergeTOML: %v", err)
	}
	if strings.Contains(string(out), "github") {
		t.Fatalf("orphaned owned key not removed:\n%s", out)
	}
	if !strings.Contains(string(out), "gpt-5.5") {
		t.Fatalf("foreign key removed during cleanup:\n%s", out)
	}
}

// TestMergeTOML_RemovesOrphanedHookKey proves hook orphan cleanup works through
// the real MergeTOML path: an owned `/hooks/<event>` pointer absent from `ours`
// is deleted while a foreign top-level key survives. This is the coverage the
// removed hand-set renderHooks OwnedKeys loop never actually exercised (render.Plan
// populates OwnedKeys from state, so the adapter-set value was always overwritten),
// and the hooks complement to TestMergeTOML_RemovesOrphanedOwnedKeys (mcp_servers).
func TestMergeTOML_RemovesOrphanedHookKey(t *testing.T) {
	existing := []byte(`model = "gpt-5.5"

[[hooks.PreToolUse]]
matcher = "Bash"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "echo hi"
`)
	// ours no longer contains hooks.PreToolUse; the owned pointer drives removal.
	ours := map[string]any{"hooks": map[string]any{}}
	out, err := codex.MergeTOML(existing, ours, []string{"/hooks/PreToolUse"})
	if err != nil {
		t.Fatalf("MergeTOML: %v", err)
	}
	if strings.Contains(string(out), "PreToolUse") {
		t.Fatalf("orphaned hook event not removed:\n%s", out)
	}
	if !strings.Contains(string(out), "gpt-5.5") {
		t.Fatalf("foreign key removed during hook cleanup:\n%s", out)
	}
}

// TestMergeTOML_EmptyExisting handles a first apply with no config.toml yet.
func TestMergeTOML_EmptyExisting(t *testing.T) {
	ours := map[string]any{
		"mcp_servers": map[string]any{
			"github": map[string]any{"command": "npx"},
		},
	}
	out, err := codex.MergeTOML(nil, ours, nil)
	if err != nil {
		t.Fatalf("MergeTOML: %v", err)
	}
	var got map[string]any
	if err := toml.Unmarshal(out, &got); err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	servers := got["mcp_servers"].(map[string]any)
	if servers["github"] == nil {
		t.Fatalf("github server missing: %v", got)
	}
}

// TestMergeTOML_PreservesNumericTypesFromJSONNumbers guards against the
// BLOCKER 1 bug where go-toml rendered json.Number as a string, corrupting
// hook timeouts and MCP extras into values Codex rejects.
func TestMergeTOML_PreservesNumericTypesFromJSONNumbers(t *testing.T) {
	ours := map[string]any{
		"mcp_servers": map[string]any{
			"github": map[string]any{
				"command":             "npx",
				"startup_timeout_sec": json.Number("10"),
			},
		},
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "echo hi",
							"timeout": json.Number("30"),
						},
					},
				},
			},
		},
	}
	out, err := codex.MergeTOML(nil, ours, nil)
	if err != nil {
		t.Fatalf("MergeTOML: %v", err)
	}
	outStr := string(out)
	if strings.Contains(outStr, "'30'") || strings.Contains(outStr, `"30"`) {
		t.Fatalf("timeout was rendered as string in TOML: %s", outStr)
	}
	if strings.Contains(outStr, "'10'") || strings.Contains(outStr, `"10"`) {
		t.Fatalf("startup_timeout_sec was rendered as string in TOML: %s", outStr)
	}
	// Verify it unmarshals with integer values:
	var parsed map[string]any
	if err := toml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Stepwise rather than a bare assertion chain, which panics on a shape
	// change instead of naming the step that broke.
	servers, ok := parsed["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_servers is %T, want a table\n%s", parsed["mcp_servers"], out)
	}
	mcp, ok := servers["github"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_servers.github is %T, want a table\n%s", servers["github"], out)
	}
	if mcp["startup_timeout_sec"] != int64(10) {
		t.Fatalf("startup_timeout_sec type mismatch: %#v (%T)", mcp["startup_timeout_sec"], mcp["startup_timeout_sec"])
	}
	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks is %T, want a table\n%s", parsed["hooks"], out)
	}
	defs, ok := hooks["PreToolUse"].([]any)
	if !ok || len(defs) == 0 {
		t.Fatalf("hooks.PreToolUse is %T (len 0?), want a non-empty array of tables\n%s", hooks["PreToolUse"], out)
	}
	hooksTable, ok := defs[0].(map[string]any)
	if !ok {
		t.Fatalf("hooks.PreToolUse[0] is %T, want a table\n%s", defs[0], out)
	}
	handlers, ok := hooksTable["hooks"].([]any)
	if !ok || len(handlers) == 0 {
		t.Fatalf("hooks.PreToolUse[0].hooks is %T (len 0?), want a non-empty array\n%s", hooksTable["hooks"], out)
	}
	handler, ok := handlers[0].(map[string]any)
	if !ok {
		t.Fatalf("hooks.PreToolUse[0].hooks[0] is %T, want a table\n%s", handlers[0], out)
	}
	if handler["timeout"] != int64(30) {
		t.Fatalf("hook timeout type mismatch: %#v (%T)", handler["timeout"], handler["timeout"])
	}
}
