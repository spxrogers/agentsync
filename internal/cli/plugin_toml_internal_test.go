package cli

import (
	"os"
	"path/filepath"
	"testing"

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
