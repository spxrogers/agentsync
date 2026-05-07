package claude_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
)

func TestApply_NewSettings_WritesContent(t *testing.T) {
	tmp := t.TempDir()
	a := claude.New(claude.Options{TargetRoot: tmp})

	op := adapter.FileOp{
		Action:        "write",
		Path:          filepath.Join(tmp, ".claude.json"),
		Content:       []byte(`{"mcpServers":{"github":{"command":"npx"}}}`),
		Mode:          0o644,
		MergeStrategy: "merge-json-keys",
	}
	if err := a.Apply([]adapter.FileOp{op}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(op.Path)
	if !strings.Contains(string(body), `"github"`) {
		t.Fatalf("missing github key: %s", body)
	}
}

func TestApply_PreservesForeignKeys(t *testing.T) {
	tmp := t.TempDir()
	a := claude.New(claude.Options{TargetRoot: tmp})
	target := filepath.Join(tmp, ".claude.json")

	// pre-existing foreign content
	_ = os.WriteFile(target, []byte(`{"foreign":{"x":1},"mcpServers":{"old":{}}}`), 0o644)

	op := adapter.FileOp{
		Action:        "write",
		Path:          target,
		Content:       []byte(`{"mcpServers":{"new":{"command":"x"}}}`),
		Mode:          0o644,
		MergeStrategy: "merge-json-keys",
		OwnedKeys:     nil, // no owned keys -> no orphan removal
	}
	if err := a.Apply([]adapter.FileOp{op}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	body, _ := os.ReadFile(target)
	_ = json.Unmarshal(body, &out)
	if _, ok := out["foreign"]; !ok {
		t.Fatalf("foreign key dropped: %s", body)
	}
	s := out["mcpServers"].(map[string]any)
	if _, ok := s["old"]; !ok {
		t.Fatalf("foreign mcpServers.old dropped: %s", body)
	}
	if _, ok := s["new"]; !ok {
		t.Fatalf("our mcpServers.new missing: %s", body)
	}
}

func TestApply_OrphanRemoval(t *testing.T) {
	tmp := t.TempDir()
	a := claude.New(claude.Options{TargetRoot: tmp})
	target := filepath.Join(tmp, ".claude.json")
	_ = os.WriteFile(target, []byte(`{"mcpServers":{"github":{"command":"old"},"stale":{}}}`), 0o644)

	op := adapter.FileOp{
		Action:        "write",
		Path:          target,
		Content:       []byte(`{"mcpServers":{"github":{"command":"new"}}}`),
		Mode:          0o644,
		MergeStrategy: "merge-json-keys",
		OwnedKeys:     []string{"/mcpServers/github", "/mcpServers/stale"},
	}
	if err := a.Apply([]adapter.FileOp{op}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	body, _ := os.ReadFile(target)
	_ = json.Unmarshal(body, &out)
	s := out["mcpServers"].(map[string]any)
	if _, ok := s["stale"]; ok {
		t.Fatalf("stale should be deleted: %s", body)
	}
	if s["github"].(map[string]any)["command"] != "new" {
		t.Fatalf("github should be updated: %s", body)
	}
}
