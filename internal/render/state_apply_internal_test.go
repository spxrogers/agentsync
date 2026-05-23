package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/state"
)

// TestRecordOpsState_SkipsAbsentMergePointers is the regression for the
// partial-apply ownership-over-recording bug. When one agent emits several
// merge-json-keys ops to the SAME destination and an early op succeeds while a
// later one fails, the apply-error rescue (saveBestEffortState) records every
// op whose PATH was written — including the failed op, since `wrote` is
// per-path. RecordOpsState then re-read the file (holding only the succeeded
// op's keys) and recorded the failed op's pointer as owned with a hash of
// null. On the next apply that pointer is treated as owned, suppressing its
// foreign-collision backup and silently overwriting a value the user created
// in between. RecordOpsState must record only pointers actually present on
// disk.
func TestRecordOpsState_SkipsAbsentMergePointers(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "settings.json")
	// On-disk reality after a partial apply: only op1's "a" landed.
	if err := os.WriteFile(dest, []byte(`{"mcpServers":{"a":{"command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ops := []adapter.FileOp{
		{Path: dest, MergeStrategy: "merge-json-keys", Content: []byte(`{"mcpServers":{"a":{"command":"x"}}}`)},
		// op that "failed": its key "b" is NOT on disk.
		{Path: dest, MergeStrategy: "merge-json-keys", Content: []byte(`{"mcpServers":{"b":{"command":"y"}}}`)},
	}

	s := state.New()
	if err := RecordOpsState(s, "", "claude", adapter.ScopeUser, "", ops); err != nil {
		t.Fatalf("RecordOpsState: %v", err)
	}

	wantOwned := "claude:user::" + dest + ":/mcpServers/a"
	wantAbsent := "claude:user::" + dest + ":/mcpServers/b"
	if _, ok := s.Keys[wantOwned]; !ok {
		t.Fatalf("present pointer /mcpServers/a was not recorded as owned; keys=%v", s.Keys)
	}
	if _, ok := s.Keys[wantAbsent]; ok {
		t.Fatalf("absent pointer /mcpServers/b was wrongly recorded as owned (would suppress a future backup); keys=%v", s.Keys)
	}
}
