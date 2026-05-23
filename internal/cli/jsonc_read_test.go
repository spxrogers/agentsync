package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadJSONFile_ParsesJSONC is the regression for drift misclassification
// on hand-commented opencode.json. Apply/Ingest accept JSONC via hujson, so a
// user may legitimately add `//` comments; readJSONFile (used by status/diff/
// reconcile to read the on-disk dest) used plain encoding/json, which rejects
// JSONC. The parse failed → empty map → every owned merge-jsonc-keys pointer
// classified as a phantom Conflict.
func TestReadJSONFile_ParsesJSONC(t *testing.T) {
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
	m := readJSONFile(p)
	mcp, ok := m["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("readJSONFile failed to parse JSONC: %v", m)
	}
	if _, ok := mcp["github"]; !ok {
		t.Fatalf("github server missing after JSONC parse: %v", mcp)
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
