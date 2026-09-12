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
	// Created out of order on purpose; the result must not depend on it. Each
	// file carries its own name as content so the content check below can tell
	// a rename from a rewrite.
	for _, name := range []string{"zeta.md", "alpha.md", "mid.md"} {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte("body of "+name+"\n"), 0o644); err != nil {
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
		data, err := os.ReadFile(filepath.Join(home, source.SubagentsDir, name))
		if err != nil {
			t.Errorf("%s did not arrive under %s: %v", name, source.SubagentsDir, err)
			continue
		}
		if string(data) != "body of "+name+"\n" {
			t.Errorf("%s arrived with the wrong content %q: want a move, not a rewrite", name, data)
		}
	}
	// Removal of the legacy dir is best-effort (a stray non-.md file keeps it);
	// the move emptied it here, so the best effort must have succeeded.
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("the emptied %s dir should be gone: stat = %v", source.LegacySubagentsDir, err)
	}
}

// TestMigrateSubagentTree_KeepsLegacyDirWithStrayFile pins the other half of
// the best-effort removal: a file the gate never looks at (anything but *.md)
// keeps the legacy dir, untouched. The removal is os.Remove precisely so that
// this holds — an os.RemoveAll would move the subagents and then delete a
// user's notes with the directory, which no test caught before this one.
func TestMigrateSubagentTree_KeepsLegacyDirWithStrayFile(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	legacy := filepath.Join(home, source.LegacySubagentsDir)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"alpha.md":  "body of alpha.md\n",
		"notes.txt": "mine, not a subagent\n",
	} {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := source.MigrateSubagentTree(home)
	if err != nil {
		t.Fatalf("MigrateSubagentTree: %v", err)
	}
	if want := []string{"alpha.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("moved = %v, want %v (the stray file is not a subagent)", got, want)
	}
	if _, err := os.Stat(filepath.Join(home, source.SubagentsDir, "alpha.md")); err != nil {
		t.Errorf("alpha.md did not arrive under %s: %v", source.SubagentsDir, err)
	}
	data, err := os.ReadFile(filepath.Join(legacy, "notes.txt"))
	if err != nil {
		t.Fatalf("the stray file must survive in %s, and the dir with it: %v", source.LegacySubagentsDir, err)
	}
	if string(data) != "mine, not a subagent\n" {
		t.Errorf("notes.txt = %q, want it untouched", data)
	}
}
