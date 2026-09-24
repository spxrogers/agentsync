package cursor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestReadJSONFile_ReadErrorIsSurfaced pins the difference between "absent" and
// "present but unreadable" for the JSON merge target.
//
// readJSONFile used to return an empty map on ANY error. applyWrite then merged
// agentsync's keys into that empty map and atomically wrote the result, so a
// settings.json that existed but could not be read was REPLACED by a file
// containing only agentsync's keys — silently destroying the user's own
// configuration. Only a genuine PARSE failure may still degrade to empty.
//
// A directory at the path is the portable stand-in for unreadable: it is
// present, it is not a regular file, and unlike a 0000-mode file it stays
// unreadable when the test runs as root (which it does, in the container).
func TestReadJSONFile_ReadErrorIsSurfaced(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	unreadable := filepath.Join(dir, "settings.json")
	if err := os.Mkdir(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := readJSONFile(unreadable); err == nil {
		t.Fatal("a present-but-unreadable merge target must surface an error; " +
			"swallowing it into an empty map makes the next write replace the user's config")
	}

	// Control 1: absent is NOT an error — it is the ordinary first-apply case.
	absent := filepath.Join(dir, "missing.json")
	got, err := readJSONFile(absent)
	if err != nil {
		t.Fatalf("an absent merge target must not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an absent merge target must decode as empty, got %v", got)
	}

	// Control 2: a present file that is not valid JSON still degrades to empty
	// rather than failing the apply.
	garbage := filepath.Join(dir, "garbage.json")
	if err := os.WriteFile(garbage, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readJSONFile(garbage); err != nil {
		t.Fatalf("a parse failure must still degrade to empty, not error: %v", err)
	}
}
