package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// TestStatus_OwnershipSurvivesLegacyStateMigration is the assertion that the
// schema 1 -> 2 key migration preserved MEANING, not just shape.
//
// A user upgrading into this binary has a v1 targets.json on disk. If the
// migration produced keys that no longer match what stateFileKey computes, every
// managed destination would classify as `foreign-collision` on the next run:
// agentsync would back up and rewrite the user's entire config. So: seed a real
// v1 file, load it through the real state.Load, and assert `status` sees the
// destination as CLEAN.
func TestStatus_OwnershipSurvivesLegacyStateMigration(t *testing.T) {
	userHome := t.TempDir()
	dest := filepath.Join(userHome, ".claude", "CLAUDE.md")
	const content = "# managed by agentsync\n"
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// A schema_version 1 file, keyed the way every pre-upgrade agentsync wrote it.
	legacy := fmt.Sprintf(
		`{"schema_version":1,"files":{"claude:user::${HOME}/.claude/CLAUDE.md":`+
			`{"sha256":%q,"mode":420,"applied_at":"2026-05-04T10:00:00Z","source_id":"memory/AGENTS.md"}}}`,
		hashContent([]byte(content)),
	)
	statePath := filepath.Join(t.TempDir(), "targets.json")
	if err := os.WriteFile(statePath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	// Sanity: the fixture really is the v1 shape. Read the BYTES back from the
	// file state.Load will open, so the oracle is what is on disk rather than
	// the string this test happens to hold.
	onDisk, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(onDisk, &probe); err != nil || probe.SchemaVersion != 1 {
		t.Fatalf("fixture is not a schema_version 1 document: %v %+v", err, probe)
	}

	s, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("Load legacy state: %v", err)
	}
	if s.SchemaVersion != state.SchemaVersion {
		t.Fatalf("state was not migrated: schema_version=%d", s.SchemaVersion)
	}

	plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{
		"claude": {Ops: []adapter.FileOp{
			{Action: "write", Path: dest, Content: []byte(content), Mode: 0o644, SourceID: "memory/AGENTS.md"},
		}},
	}}
	model := buildStatusModel(plan, []string{"claude"}, s, userHome, adapter.ScopeUser, "")
	if got := model.Summary["clean"]; got != 1 {
		t.Fatalf("a destination owned in a MIGRATED v1 state must classify clean, "+
			"not force a foreign-collision backup of the user's whole config; summary=%v",
			model.Summary)
	}
}
