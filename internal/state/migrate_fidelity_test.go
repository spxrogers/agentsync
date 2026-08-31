package state_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/state"
)

// v1Fixture is a spec-complete schema_version-1 targets.json: all four maps
// populated, files and keys entries with every field set, a user-scope key, a
// project-scope key, a colon-bearing project root (the issue #227 shape), and a
// pointer key.
const v1Fixture = `{
  "schema_version": 1,
  "files": {
    "claude:user::${HOME}/.claude/settings.json": {
      "sha256": "aaa",
      "mode": 420,
      "applied_at": "2026-05-04T10:00:00Z",
      "source_id": "memory/AGENTS.md"
    },
    "claude:project:${HOME}/work/app:staging:${HOME}/work/app:staging/.mcp.json": {
      "sha256": "bbb",
      "mode": 420,
      "applied_at": "2026-05-04T10:00:01Z",
      "source_id": "mcp/github.toml"
    }
  },
  "keys": {
    "claude:user::${HOME}/.claude.json:/mcpServers/github": {
      "sha256": "ccc",
      "applied_at": "2026-05-04T10:00:02Z",
      "source_id": "mcp/* (multiple)"
    }
  },
  "marketplaces": {
    "anthropic": {"url": "https://example/x.git", "ref": "main", "head_sha": "deadbeef", "fetched_at": "2026-05-04T09:00:00Z"}
  },
  "plugins": {
    "anthropic/atlassian": {"version": "1.2.3", "manifest_sha": "tree:v1:abc", "enabled": true}
  }
}`

// TestMigrate_V1FixtureUpgradesOnDisk drives Load → Save over a real v1 file and
// asserts against the BYTES that come back, not the struct: schema_version is
// stamped 2, every key is re-encoded as a state.Key, every entry payload
// survives unchanged, and a second Load is a fixpoint.
func TestMigrate_V1FixtureUpgradesOnDisk(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "targets.json")
	if err := os.WriteFile(p, []byte(v1Fixture), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := state.Load(p)
	if err != nil {
		t.Fatalf("Load v1 fixture: %v", err)
	}
	if loaded.SchemaVersion != state.SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", loaded.SchemaVersion, state.SchemaVersion)
	}

	wantFiles := map[state.Key]string{
		{Agent: "claude", Scope: "user", Path: "${HOME}/.claude/settings.json"}: "aaa",
		{
			Agent: "claude", Scope: "project",
			Project: "${HOME}/work/app:staging",
			Path:    "${HOME}/work/app:staging/.mcp.json",
		}: "bbb",
	}
	if len(loaded.Files) != len(wantFiles) {
		t.Fatalf("Files has %d entries, want %d: %+v", len(loaded.Files), len(wantFiles), loaded.Files)
	}
	for k, sha := range wantFiles {
		got, ok := loaded.Files[k]
		if !ok {
			t.Fatalf("migrated Files is missing %+v; have %+v", k, loaded.Files)
		}
		if got.SHA256 != sha || got.Mode != 0o644 || got.SourceID == "" || got.AppliedAt.IsZero() {
			t.Fatalf("entry payload changed for %+v: %+v", k, got)
		}
	}
	ptrKey := state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude.json", Pointer: "/mcpServers/github"}
	if got, ok := loaded.Keys[ptrKey]; !ok || got.SHA256 != "ccc" || got.SourceID != "mcp/* (multiple)" {
		t.Fatalf("migrated Keys lost the pointer entry: ok=%v got=%+v have=%+v", ok, got, loaded.Keys)
	}
	if m, ok := loaded.Marketplaces["anthropic"]; !ok || m.HeadSHA != "deadbeef" {
		t.Fatalf("marketplaces must survive verbatim: %+v", loaded.Marketplaces)
	}
	if pl, ok := loaded.Plugins["anthropic/atlassian"]; !ok || pl.Version != "1.2.3" || !pl.Enabled {
		t.Fatalf("plugins must survive verbatim: %+v", loaded.Plugins)
	}

	// Now the artifact assertion: save and re-read the FILE.
	if err := state.Save(p, loaded); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk struct {
		SchemaVersion int                        `json:"schema_version"`
		Files         map[string]json.RawMessage `json:"files"`
		Keys          map[string]json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("saved file is not valid JSON: %v\n%s", err, raw)
	}
	if onDisk.SchemaVersion != 2 {
		t.Fatalf("on-disk schema_version = %d, want 2\n%s", onDisk.SchemaVersion, raw)
	}
	if len(onDisk.Files) != 2 || len(onDisk.Keys) != 1 {
		t.Fatalf("on-disk entry counts changed: files=%d keys=%d\n%s", len(onDisk.Files), len(onDisk.Keys), raw)
	}
	for s := range onDisk.Files {
		if _, err := state.ParseKey(s); err != nil {
			t.Fatalf("on-disk files key %q is not a state.Key: %v", s, err)
		}
	}
	for s := range onDisk.Keys {
		if _, err := state.ParseKey(s); err != nil {
			t.Fatalf("on-disk keys key %q is not a state.Key: %v", s, err)
		}
	}

	// Reloading the saved file is a fixpoint — the migration is idempotent.
	again, err := state.Load(p)
	if err != nil {
		t.Fatalf("reload migrated file: %v", err)
	}
	if len(again.Files) != len(loaded.Files) || len(again.Keys) != len(loaded.Keys) {
		t.Fatalf("reload is not a fixpoint: %+v vs %+v", again, loaded)
	}
	for k, v := range loaded.Files {
		if again.Files[k] != v {
			t.Fatalf("reload changed %+v: %+v vs %+v", k, again.Files[k], v)
		}
	}
}
