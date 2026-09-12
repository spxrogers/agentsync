package source_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestMigrateSubagentTree_MovesInSortedOrder pins the on-disk half of the
// agents/ → subagents/ migration directly, and its one contract the caller
// shows the user: the moved names come back sorted (`migrate subagents` prints
// them). The order is LegacySubagentFiles' — the function itself no longer
// sorts — so this is the test that fails if either layer stops.
func TestMigrateSubagentTree_MovesInSortedOrder(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	legacy := filepath.Join(home, source.LegacySubagentsDir)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	// Created out of order on purpose; the result must not depend on it.
	for _, name := range []string{"zeta.md", "alpha.md", "mid.md"} {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte("---\nname: x\n---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := source.MigrateSubagentTree(home)
	if err != nil {
		t.Fatalf("MigrateSubagentTree: %v", err)
	}
	if want := []string{"alpha.md", "mid.md", "zeta.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("moved = %v, want sorted %v", got, want)
	}
	for _, name := range got {
		if _, err := os.Stat(filepath.Join(home, source.SubagentsDir, name)); err != nil {
			t.Errorf("%s did not arrive under %s: %v", name, source.SubagentsDir, err)
		}
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("the emptied %s dir must be gone: stat = %v", source.LegacySubagentsDir, err)
	}
}
