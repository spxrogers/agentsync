package claude_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
)

// writeSettings writes a user-scope settings.json under tmp/.claude/.
func writeSettings(t *testing.T, tmp, body string) {
	t.Helper()
	dir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestIngestPlugins_ParsesMarketplacesAndEnabled verifies the documented
// settings.json keys (extraKnownMarketplaces + enabledPlugins) decode into
// native descriptors, that disabled/false entries are marked not-enabled, and
// that results are deterministically ordered.
func TestIngestPlugins_ParsesMarketplacesAndEnabled(t *testing.T) {
	tmp := t.TempDir()
	writeSettings(t, tmp, `{
		"extraKnownMarketplaces": {
			"obra-superpowers": {"source": {"source": "github", "repo": "obra/superpowers", "ref": "main"}},
			"local-mp": {"source": {"source": "directory", "path": "/home/u/mp"}}
		},
		"enabledPlugins": {
			"superpowers@obra-superpowers": true,
			"helper@local-mp": ["partial"],
			"off@obra-superpowers": false,
			"null-plugin@local-mp": null
		}
	}`)

	a := claude.New(claude.Options{TargetRoot: tmp})
	mps, plugins, err := a.IngestPlugins(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("IngestPlugins: %v", err)
	}

	if len(mps) != 2 {
		t.Fatalf("want 2 marketplaces, got %+v", mps)
	}
	// Sorted by ID: local-mp, obra-superpowers.
	if mps[0].ID != "local-mp" || mps[0].Source.Type != "directory" || mps[0].Source.Path != "/home/u/mp" {
		t.Errorf("local-mp parsed wrong: %+v", mps[0])
	}
	if mps[1].ID != "obra-superpowers" || mps[1].Source.Type != "github" ||
		mps[1].Source.Repo != "obra/superpowers" || mps[1].Source.Ref != "main" {
		t.Errorf("obra-superpowers parsed wrong: %+v", mps[1])
	}

	// Sorted by (name, marketplace): helper, null-plugin, off, superpowers.
	want := []adapter.NativePlugin{
		{Name: "helper", MarketplaceID: "local-mp", Enabled: true},       // non-empty array → enabled
		{Name: "null-plugin", MarketplaceID: "local-mp", Enabled: false}, // null → disabled
		{Name: "off", MarketplaceID: "obra-superpowers", Enabled: false}, // false → disabled
		{Name: "superpowers", MarketplaceID: "obra-superpowers", Enabled: true},
	}
	if len(plugins) != len(want) {
		t.Fatalf("want %d plugins, got %+v", len(want), plugins)
	}
	for i, w := range want {
		if plugins[i] != w {
			t.Errorf("plugin[%d] = %+v, want %+v", i, plugins[i], w)
		}
	}
}

// TestIngestPlugins_MissingSettings returns empty (not an error) when there is
// no settings.json, so a full `import` of a plugin-free agent stays clean.
func TestIngestPlugins_MissingSettings(t *testing.T) {
	tmp := t.TempDir()
	a := claude.New(claude.Options{TargetRoot: tmp})
	mps, plugins, err := a.IngestPlugins(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("IngestPlugins: %v", err)
	}
	if mps != nil || plugins != nil {
		t.Fatalf("want nil/nil for missing settings; got %+v / %+v", mps, plugins)
	}
}

// TestIngestPlugins_MalformedIsLenient treats an unparseable settings.json as
// "no plugins discovered" rather than failing the whole import (matching the
// hooks/LSP blocks in Ingest).
func TestIngestPlugins_MalformedIsLenient(t *testing.T) {
	tmp := t.TempDir()
	writeSettings(t, tmp, `{ this is : not json `)
	a := claude.New(claude.Options{TargetRoot: tmp})
	mps, plugins, err := a.IngestPlugins(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("malformed settings should be lenient, got error: %v", err)
	}
	if mps != nil || plugins != nil {
		t.Fatalf("want nil/nil for malformed settings; got %+v / %+v", mps, plugins)
	}
}
