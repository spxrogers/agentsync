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

// TestMergeKeys_ReplacesOwnedObjectWholesale is the regression for stale inner
// fields surviving removal. agentsync owns /mcpServers/<id> as a whole object,
// so when `ours` drops an inner key (e.g. `env`, or `command` after a
// stdio→http transport switch), MergeKeys must REPLACE the object — a
// deep-merge stranded the removed field, leaving a stale (often secret-bearing)
// value or an ambiguous command+url server on disk.
func TestMergeKeys_ReplacesOwnedObjectWholesale(t *testing.T) {
	existing := decode(`{"mcpServers": {"github": {"command": "npx", "env": {"TOKEN": "abc"}}}}`)
	ours := decode(`{"mcpServers": {"github": {"command": "npx"}}}`)
	owned := []string{"/mcpServers/github"}

	merged, _, _ := jsonkeys.MergeKeys(existing, ours, owned)
	gh := merged["mcpServers"].(map[string]any)["github"].(map[string]any)
	if _, ok := gh["env"]; ok {
		t.Fatalf("stale env survived owned-object replace: %+v", gh)
	}

	// Transport switch: command must vanish when ours has only url.
	existing2 := decode(`{"mcpServers": {"srv": {"command": "npx", "type": "stdio"}}}`)
	ours2 := decode(`{"mcpServers": {"srv": {"url": "https://x", "type": "http"}}}`)
	merged2, _, _ := jsonkeys.MergeKeys(existing2, ours2, []string{"/mcpServers/srv"})
	srv := merged2["mcpServers"].(map[string]any)["srv"].(map[string]any)
	if _, ok := srv["command"]; ok {
		t.Fatalf("stale command survived transport switch: %+v", srv)
	}
	if srv["url"] != "https://x" {
		t.Fatalf("url not written on transport switch: %+v", srv)
	}
}

// TestConvertNumbers pins the range contract stated on ConvertNumbers: integers
// within int64 stay exact (beyond float64's 2^53), an integer beyond int64
// falls to float64 (lossy, but TOML integers are int64 so exactness past that
// bound is unrepresentable anyway), and a numeric that parses as neither int64
// nor float64 (1e309) falls back to its literal string.
func TestConvertNumbers(t *testing.T) {
	cases := []struct {
		name string
		in   json.Number
		want any
	}{
		{"int64 max exact", json.Number("9223372036854775807"), int64(9223372036854775807)},
		{"2^53+1 exact beyond float64 precision", json.Number("9007199254740993"), int64(9007199254740993)},
		{"2^63 beyond int64 falls to float64", json.Number("9223372036854775808"), float64(9223372036854775808)},
		{"1e309 beyond float64 falls to literal string", json.Number("1e309"), "1e309"},
		{"fraction to float64", json.Number("0.5"), float64(0.5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := jsonkeys.ConvertNumbers(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ConvertNumbers(%q) = %v (%T); want %v (%T)", tc.in, got, got, tc.want, tc.want)
			}
		})
	}
}

// TestConvertNumbers_RecursesThroughMapsAndSlices proves the walk reaches
// json.Number values nested inside maps and slices (and leaves non-numeric
// values untouched), matching what a UseNumber-decoded Extra map holds.
func TestConvertNumbers_RecursesThroughMapsAndSlices(t *testing.T) {
	in := map[string]any{
		"timeout": json.Number("30"),
		"nested": map[string]any{
			"snowflake": json.Number("9007199254740993"),
			"list":      []any{json.Number("1"), "keep-me", json.Number("2.5")},
		},
		"untouched": "string",
	}
	want := map[string]any{
		"timeout": int64(30),
		"nested": map[string]any{
			"snowflake": int64(9007199254740993),
			"list":      []any{int64(1), "keep-me", float64(2.5)},
		},
		"untouched": "string",
	}
	got := jsonkeys.ConvertNumbers(in)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ConvertNumbers nested walk mismatch:\n got %#v\nwant %#v", got, want)
	}
}
