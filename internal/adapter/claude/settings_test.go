package claude_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter/claude"
)

func decode(s string) map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

func TestMergeKeys_NewKey(t *testing.T) {
	existing := decode(`{"foreign": {"keep": true}}`)
	ours := decode(`{"mcpServers": {"github": {"command": "npx"}}}`)

	merged, _, _ := claude.MergeKeys(existing, ours, nil)
	if _, ok := merged["foreign"]; !ok {
		t.Fatalf("foreign key dropped: %+v", merged)
	}
	if _, ok := merged["mcpServers"].(map[string]any)["github"]; !ok {
		t.Fatalf("ours not added: %+v", merged)
	}
}

func TestMergeKeys_ForeignKeyAtSameLevelPreserved(t *testing.T) {
	existing := decode(`{"mcpServers": {"foreign-server": {"command": "x"}}}`)
	ours := decode(`{"mcpServers": {"github": {"command": "npx"}}}`)

	merged, _, _ := claude.MergeKeys(existing, ours, nil)
	s := merged["mcpServers"].(map[string]any)
	if _, ok := s["foreign-server"]; !ok {
		t.Fatalf("sibling foreign mcpServers entry dropped: %+v", s)
	}
	if _, ok := s["github"]; !ok {
		t.Fatalf("our entry missing: %+v", s)
	}
}

func TestMergeKeys_OrphanRemoval(t *testing.T) {
	existing := decode(`{"mcpServers": {"github": {"command": "old"}, "stale": {"command": "x"}}}`)
	ours := decode(`{"mcpServers": {"github": {"command": "npx"}}}`)
	owned := []string{"/mcpServers/github", "/mcpServers/stale"} // both are ours per state

	merged, _, removed := claude.MergeKeys(existing, ours, owned)

	if len(removed) != 1 || removed[0] != "/mcpServers/stale" {
		t.Fatalf("expected /mcpServers/stale removed, got %v", removed)
	}
	s := merged["mcpServers"].(map[string]any)
	if _, ok := s["stale"]; ok {
		t.Fatalf("stale should be deleted: %+v", s)
	}
	if reflect.DeepEqual(s["github"].(map[string]any)["command"], "old") {
		t.Fatalf("github not updated to new value: %+v", s["github"])
	}
}

func TestMergeKeys_ForeignNotInOwnedListPreserved(t *testing.T) {
	existing := decode(`{"mcpServers": {"foreign": {"command": "x"}}}`)
	ours := decode(`{}`) // we removed everything
	owned := []string{"/mcpServers/old-mine"}

	merged, _, removed := claude.MergeKeys(existing, ours, owned)
	s := merged["mcpServers"].(map[string]any)
	if _, ok := s["foreign"]; !ok {
		t.Fatalf("foreign mcpServers entry must survive: %+v", s)
	}
	if len(removed) != 0 {
		t.Fatalf("we owned old-mine but it wasn't in existing; nothing to remove. Got %v", removed)
	}
}
