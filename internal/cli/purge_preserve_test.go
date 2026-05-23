package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPurge_PreservesUserKeysInSharedFile is the regression for the
// data-loss showstopper: `agent disable --purge` issued a whole-file delete
// for every owned dest path, including key-merge files like ~/.claude.json
// where agentsync owns only some pointers. That destroyed the user's foreign
// keys (other MCP servers, top-level settings) with no backup. Purge must
// prune only the owned pointers from a key-merge dest.
func TestPurge_PreservesUserKeysInSharedFile(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}
	mcpPath := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcpPath), 0o755)
	_ = os.WriteFile(mcpPath, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The user hand-adds foreign content to the shared dest file.
	dest := filepath.Join(tmp, ".claude.json")
	raw, _ := os.ReadFile(dest)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["userTopLevel"] = "keep-me"
	if mcp, ok := m["mcpServers"].(map[string]any); ok {
		mcp["userServer"] = map[string]any{"command": "mine"}
	}
	nb, _ := json.Marshal(m)
	_ = os.WriteFile(dest, nb, 0o644)

	if _, err := runCLI(t, env, "agent", "disable", "claude", "--purge"); err != nil {
		t.Fatalf("disable --purge: %v", err)
	}

	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("dest file was deleted entirely (user data lost): %v", err)
	}
	var am map[string]any
	if err := json.Unmarshal(after, &am); err != nil {
		t.Fatalf("dest unreadable after purge: %v\n%s", err, after)
	}
	if am["userTopLevel"] != "keep-me" {
		t.Fatalf("user top-level key destroyed by purge: %s", after)
	}
	mcp, _ := am["mcpServers"].(map[string]any)
	if _, ok := mcp["userServer"]; !ok {
		t.Fatalf("user's foreign MCP server destroyed by purge: %s", after)
	}
	if _, ok := mcp["github"]; ok {
		t.Fatalf("agentsync-owned server should have been purged: %s", after)
	}
}
