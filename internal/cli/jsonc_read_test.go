package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadDestFile_ParsesJSONC is the regression for drift misclassification
// on hand-commented opencode.json. Apply/Ingest accept JSONC via hujson, so a
// user may legitimately add `//` comments; the dest reader (used by status/diff/
// reconcile) must not use plain encoding/json, which rejects JSONC — a parse
// failure → empty map → every owned merge-jsonc-keys pointer classified as a
// phantom Conflict. readDestFile routes a non-TOML strategy through the
// JSONC-tolerant loose reader.
func TestReadDestFile_ParsesJSONC(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "opencode.json")
	content := `{
  // managed by agentsync
  "mcp": {
    "github": {"command": "npx"}, // trailing comma below too
  },
}`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := readDestFile("merge-jsonc-keys", p)
	mcp, ok := m["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("readDestFile failed to parse JSONC: %v", m)
	}
	if _, ok := mcp["github"]; !ok {
		t.Fatalf("github server missing after JSONC parse: %v", mcp)
	}
}

// TestReadDestFile_ParsesTOML covers the Codex config.toml path: a non-JSON
// dest must decode via toml.Unmarshal, not the JSON reader (which would yield an
// empty map and phantom drift for every owned merge-toml-keys pointer).
func TestReadDestFile_ParsesTOML(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "config.toml")
	content := "model = \"gpt-5.5\"\n\n[mcp_servers.github]\ncommand = \"npx\"\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := readDestFile("merge-toml-keys", p)
	servers, ok := m["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("readDestFile failed to parse TOML: %v", m)
	}
	if _, ok := servers["github"]; !ok {
		t.Fatalf("github server missing after TOML parse: %v", servers)
	}
}

// TestJSONUnmarshalLoose_ParsesJSONC covers the import-seed path, which used
// the same plain encoding/json and would mis-seed state hashes (hashing null)
// for a JSONC dest.
func TestJSONUnmarshalLoose_ParsesJSONC(t *testing.T) {
	var m map[string]any
	content := []byte("{\n  \"a\": 1, // comment\n}")
	if err := jsonUnmarshalLoose(content, &m); err != nil {
		t.Fatalf("jsonUnmarshalLoose JSONC: %v", err)
	}
	if m["a"] == nil {
		t.Fatalf("key a missing after JSONC loose-parse: %v", m)
	}
}
