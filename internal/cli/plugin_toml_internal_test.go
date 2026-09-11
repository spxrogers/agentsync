package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestReadPluginTOML_FillsIDFromFilename pins the one thing readPluginTOML does
// beyond decoding: source.Plugin.ID is toml:"-" — the id is the FILENAME, not a
// key in the file — and source.Load's loadPlugins fills it from the stem. Now
// that the CLI decodes into the same struct, it must mean the same thing there,
// or a caller reaching for .ID would silently get "" for a plugin that has one.
func TestReadPluginTOML_FillsIDFromFilename(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.toml")
	if err := os.WriteFile(path, []byte("[plugin]\nid = 'demo@test-mp'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readPluginTOML(path)
	if err != nil {
		t.Fatalf("readPluginTOML: %v", err)
	}
	if got.ID.Unverified() != "demo" {
		t.Errorf("Plugin.ID = %q, want the filename stem %q (source.Load fills it the same way)", got.ID.Unverified(), "demo")
	}
	// The inner spec id is the marketplace-qualified ref and is a different
	// thing; the two must not be confused for one another.
	if got.Plugin.ID.Unverified() != "demo@test-mp" {
		t.Errorf("PluginSpec.ID = %q, want the recorded ref %q", got.Plugin.ID.Unverified(), "demo@test-mp")
	}
}

// TestReadPluginTOML_OuterIDNeverReachesTheFile pins the tag the stem fill
// relies on. readPluginTOML sets source.Plugin.ID, and the lifecycle verbs
// re-marshal that whole struct, so a top-level `id` key stays out of the file
// ONLY because the field is toml:"-". Losing the tag would write `id = 'demo'`
// above [plugin] on the next `plugin disable`, with every other test green.
func TestReadPluginTOML_OuterIDNeverReachesTheFile(t *testing.T) {
	testenv.RequireContainer(t)
	path := filepath.Join(t.TempDir(), "demo.toml")
	canonical, err := toml.Marshal(source.Plugin{Plugin: source.PluginSpec{
		ID: "demo@test-mp", Update: "track", Agents: []string{"*"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readPluginTOML(path)
	if err != nil {
		t.Fatalf("readPluginTOML: %v", err)
	}
	if got.ID.Unverified() == "" {
		t.Fatal("precondition: readPluginTOML must have filled Plugin.ID for this test to prove anything")
	}
	again, err := toml.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, canonical) {
		t.Errorf("re-marshalling what readPluginTOML returned changed the bytes:\n--- canonical\n%s--- re-marshalled\n%s", canonical, again)
	}
	if strings.Contains(string(again), "id = 'demo'\n") {
		t.Errorf("the filename-derived Plugin.ID must never be written as a key:\n%s", again)
	}
}

// TestReadPluginTOML_ToleratesUnknownKeys pins the permissive decode the doc
// comment promises: a lifecycle verb must be able to read (and rewrite) a
// hand-edited registry file whose stray key source.Load would reject.
func TestReadPluginTOML_ToleratesUnknownKeys(t *testing.T) {
	testenv.RequireContainer(t)
	path := filepath.Join(t.TempDir(), "demo.toml")
	if err := os.WriteFile(path, []byte("[plugin]\nid = 'demo@test-mp'\nfuture_key = 'kept by nobody'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readPluginTOML(path)
	if err != nil {
		t.Fatalf("a stray key must not fail the lifecycle read: %v", err)
	}
	if got.Plugin.ID.Unverified() != "demo@test-mp" {
		t.Errorf("PluginSpec.ID = %q, want %q", got.Plugin.ID.Unverified(), "demo@test-mp")
	}
}
