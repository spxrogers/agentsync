package opencode_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
)

func TestApply_WritesNewFile(t *testing.T) {
	tmp := t.TempDir()
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	path := filepath.Join(tmp, ".config", "opencode", "opencode.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)

	op := adapter.FileOp{
		Action:        "write",
		Path:          path,
		Content:       []byte(`{"mcp":{"github":{"command":"npx"}}}`),
		Mode:          0o644,
		MergeStrategy: "merge-jsonc-keys",
	}
	if err := a.Apply([]adapter.FileOp{op}, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), `"github"`) {
		t.Fatalf("missing github key: %s", body)
	}
}

func TestApply_JSONC_PreservesForeignKeysAndComments(t *testing.T) {
	tmp := t.TempDir()
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	path := filepath.Join(tmp, "opencode.json")

	// Pre-existing JSONC with a comment and a foreign key.
	existing := `{
  // user comment
  "theme": "dark",
  "mcp": {"old": {}}
}`
	_ = os.WriteFile(path, []byte(existing), 0o644)

	op := adapter.FileOp{
		Action:        "write",
		Path:          path,
		Content:       []byte(`{"mcp":{"github":{"command":"npx"}}}`),
		Mode:          0o644,
		MergeStrategy: "merge-jsonc-keys",
		OwnedKeys:     nil, // no owned -> no orphan removal
	}
	if err := a.Apply([]adapter.FileOp{op}, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	body, _ := os.ReadFile(path)
	_ = json.Unmarshal(body, &out)
	if out["theme"] != "dark" {
		t.Fatalf("foreign key 'theme' dropped: %s", body)
	}
	mcp := out["mcp"].(map[string]any)
	if _, ok := mcp["old"]; !ok {
		t.Fatalf("foreign mcp.old dropped: %s", body)
	}
	if _, ok := mcp["github"]; !ok {
		t.Fatalf("our mcp.github missing: %s", body)
	}
}

func TestApply_JSONC_OrphanRemoval(t *testing.T) {
	tmp := t.TempDir()
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	path := filepath.Join(tmp, "opencode.json")
	_ = os.WriteFile(path, []byte(`{"mcp":{"github":{},"stale":{}}}`), 0o644)

	op := adapter.FileOp{
		Action:        "write",
		Path:          path,
		Content:       []byte(`{"mcp":{"github":{"command":"npx"}}}`),
		Mode:          0o644,
		MergeStrategy: "merge-jsonc-keys",
		OwnedKeys:     []string{"/mcp/github", "/mcp/stale"},
	}
	if err := a.Apply([]adapter.FileOp{op}, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	body, _ := os.ReadFile(path)
	_ = json.Unmarshal(body, &out)
	mcp := out["mcp"].(map[string]any)
	if _, ok := mcp["stale"]; ok {
		t.Fatalf("stale should be deleted: %s", body)
	}
	if mcp["github"].(map[string]any)["command"] != "npx" {
		t.Fatalf("github not updated: %s", body)
	}
}

func TestApply_Delete_RemovesFile(t *testing.T) {
	tmp := t.TempDir()
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	path := filepath.Join(tmp, "todelete.txt")
	_ = os.WriteFile(path, []byte("bye"), 0o644)

	op := adapter.FileOp{Action: "delete", Path: path}
	if err := a.Apply([]adapter.FileOp{op}, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should be gone")
	}
}

func TestApply_Delete_MissingFileNoError(t *testing.T) {
	tmp := t.TempDir()
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	op := adapter.FileOp{Action: "delete", Path: filepath.Join(tmp, "nonexistent.txt")}
	if err := a.Apply([]adapter.FileOp{op}, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("delete missing file should not error: %v", err)
	}
}
