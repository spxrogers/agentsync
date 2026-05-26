package codex_test

import (
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
