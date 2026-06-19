package codex_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
)

func writeConfig(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIngestPlugins_ParsesEnableState(t *testing.T) {
	tmp := t.TempDir()
	writeConfig(t, tmp, `model = "gpt-5.5"

[plugins."gmail@openai-curated"]
enabled = false

[plugins."github@openai-curated"]
enabled = true

# present table, no explicit enabled => default-on
[plugins."slack@team-mp"]
`)
	a := codex.New(codex.Options{TargetRoot: tmp})
	mps, plugins, err := a.IngestPlugins(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("IngestPlugins: %v", err)
	}
	// Codex doesn't record marketplace fetch sources, so none are emitted.
	if len(mps) != 0 {
		t.Fatalf("expected no native marketplaces, got %+v", mps)
	}
	// Sorted by (name, marketplace): github, gmail, slack.
	if len(plugins) != 3 {
		t.Fatalf("expected 3 plugins, got %+v", plugins)
	}
	want := map[string]struct {
		mp      string
		enabled bool
	}{
		"github": {"openai-curated", true},
		"gmail":  {"openai-curated", false},
		"slack":  {"team-mp", true},
	}
	for _, pl := range plugins {
		w, ok := want[pl.Name.Unverified()]
		if !ok {
			t.Fatalf("unexpected plugin %q", pl.Name)
		}
		if pl.MarketplaceID != w.mp {
			t.Fatalf("%s marketplace = %q, want %q", pl.Name, pl.MarketplaceID, w.mp)
		}
		if pl.Enabled != w.enabled {
			t.Fatalf("%s enabled = %v, want %v", pl.Name, pl.Enabled, w.enabled)
		}
	}
}

func TestIngestPlugins_MissingConfig(t *testing.T) {
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	mps, plugins, err := a.IngestPlugins(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("IngestPlugins on missing config should not error: %v", err)
	}
	if len(mps) != 0 || len(plugins) != 0 {
		t.Fatalf("expected nothing from missing config, got mps=%+v plugins=%+v", mps, plugins)
	}
}
