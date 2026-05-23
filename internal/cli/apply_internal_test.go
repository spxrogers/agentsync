package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// TestSaveBestEffortState_OnlyRecordsWrittenPaths is the regression for the
// partial-apply data-loss edge: render.Apply stops at the first failing op, so
// ops after it are never attempted. saveBestEffortState must record only paths
// agentsync actually wrote this run — never a pre-existing FOREIGN file sitting
// at an unattempted op's path. Recording such a file as owned would suppress
// its foreign-collision backup on the next apply and silently destroy the
// user's data. The previous os.Stat-based check recorded any op whose dest
// merely existed, foreign or not.
func TestSaveBestEffortState_OnlyRecordsWrittenPaths(t *testing.T) {
	tmp := t.TempDir()
	writtenPath := filepath.Join(tmp, "written.json")
	foreignPath := filepath.Join(tmp, "foreign.json")
	// Both files exist on disk: one because agentsync wrote it this run, the
	// other because it was already there and its op was never attempted.
	if err := os.WriteFile(writtenPath, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignPath, []byte(`{"user":"keep me"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{
		"claude": {Ops: []adapter.FileOp{
			{Action: "write", Path: writtenPath, Content: []byte(`{"a":1}`)},
			{Action: "write", Path: foreignPath, Content: []byte(`{"a":2}`)},
		}},
	}}

	st := state.New()
	statePath := filepath.Join(tmp, "targets.json")
	// Only the first op was attempted+written before the apply failed.
	wrote := map[string]bool{writtenPath: true}

	if err := saveBestEffortState(st, statePath, plan, tmp, adapter.ScopeUser, "", wrote); err != nil {
		t.Fatalf("saveBestEffortState: %v", err)
	}

	if !fileKeyRecorded(st, "written.json") {
		t.Errorf("the written path must be recorded as owned (its foreign content was backed up at write time)")
	}
	if fileKeyRecorded(st, "foreign.json") {
		t.Errorf("SECURITY: an unattempted foreign file was recorded as owned — its backup would be suppressed and the user's data lost on the next apply")
	}
}

func fileKeyRecorded(st *state.Targets, nameSubstr string) bool {
	for k := range st.Files {
		if strings.Contains(k, nameSubstr) {
			return true
		}
	}
	return false
}
