package state_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/state"
)

func TestStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "targets.json")

	in := state.New()
	in.Files["claude:user::~/.claude/settings.json"] = state.FileEntry{
		SHA256:    "abc",
		Mode:      0o644,
		AppliedAt: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		SourceID:  "mcp/github.toml",
	}

	if err := state.Save(p, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := state.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := out.Files["claude:user::~/.claude/settings.json"]
	if got.SHA256 != "abc" || got.SourceID != "mcp/github.toml" {
		t.Fatalf("entry round-trip lost data: %+v", got)
	}
	if out.SchemaVersion != state.SchemaVersion {
		t.Fatalf("schema_version = %d", out.SchemaVersion)
	}
}

func TestStore_LoadMissingReturnsNew(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing.json")
	s, err := state.Load(p)
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if s.SchemaVersion != state.SchemaVersion {
		t.Fatalf("missing-load did not produce a fresh state: %+v", s)
	}
}

func TestStore_AtomicReplaceLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "targets.json")
	if err := state.Save(p, state.New()); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(p, state.New()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d: %+v", len(entries), entries)
	}
}
