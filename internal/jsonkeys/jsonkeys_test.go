package jsonkeys_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

func decode(s string) map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

func TestMergeKeys_NewKey(t *testing.T) {
	existing := decode(`{"foreign": {"keep": true}}`)
	ours := decode(`{"mcpServers": {"github": {"command": "npx"}}}`)

	merged, _, _ := jsonkeys.MergeKeys(existing, ours, nil)
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

	merged, _, _ := jsonkeys.MergeKeys(existing, ours, nil)
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

	merged, _, removed := jsonkeys.MergeKeys(existing, ours, owned)

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

	merged, _, removed := jsonkeys.MergeKeys(existing, ours, owned)
	s := merged["mcpServers"].(map[string]any)
	if _, ok := s["foreign"]; !ok {
		t.Fatalf("foreign mcpServers entry must survive: %+v", s)
	}
	if len(removed) != 0 {
		t.Fatalf("we owned old-mine but it wasn't in existing; nothing to remove. Got %v", removed)
	}
}

// TestMergeKeys_DeepCopiesArrays guards against deepCopyMap aliasing nested
// arrays (and the objects inside them) from the input `existing` map. Today no
// caller mutates a merged array post-merge, but the aliasing is a latent
// footgun: a future caller that did would silently corrupt the on-disk-derived
// input. MergeKeys must return a fully independent copy.
func TestMergeKeys_DeepCopiesArrays(t *testing.T) {
	existing := map[string]any{
		"hooks": []any{map[string]any{"cmd": "original"}},
	}
	merged, _, _ := jsonkeys.MergeKeys(existing, map[string]any{"other": float64(1)}, nil)

	arr, ok := merged["hooks"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("hooks array not preserved through merge: %+v", merged["hooks"])
	}
	elem := arr[0].(map[string]any)
	elem["cmd"] = "mutated"

	origElem := existing["hooks"].([]any)[0].(map[string]any)
	if origElem["cmd"] != "original" {
		t.Fatalf("mutating the merged array corrupted the input `existing`: %v", origElem)
	}
}
