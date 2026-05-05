package marketplace_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestComputePendingBumps_TrackMode_Bumps(t *testing.T) {
	plugins := []source.Plugin{
		{
			ID: "demo",
			Plugin: source.PluginSpec{
				ID:      "demo@test-mp",
				Version: "1.0.0",
				Update:  "track",
			},
		},
	}

	fetched := map[string]map[string]marketplace.PluginEntry{
		"test-mp": {
			"demo": {Name: "demo", Version: "1.1.0"},
		},
	}

	bumps := marketplace.ComputePendingBumps(nil, nil, plugins, fetched)
	if len(bumps) != 1 {
		t.Fatalf("expected 1 bump, got %d", len(bumps))
	}
	b := bumps[0]
	if b.ID != "demo" {
		t.Errorf("bump ID = %q, want demo", b.ID)
	}
	if b.From != "1.0.0" {
		t.Errorf("bump From = %q, want 1.0.0", b.From)
	}
	if b.To != "1.1.0" {
		t.Errorf("bump To = %q, want 1.1.0", b.To)
	}
}

func TestComputePendingBumps_PinnedMode_NoBump(t *testing.T) {
	plugins := []source.Plugin{
		{
			ID: "pinned-plugin",
			Plugin: source.PluginSpec{
				ID:      "pinned-plugin@test-mp",
				Version: "1.0.0",
				Update:  "pinned",
			},
		},
	}

	fetched := map[string]map[string]marketplace.PluginEntry{
		"test-mp": {
			"pinned-plugin": {Name: "pinned-plugin", Version: "2.0.0"},
		},
	}

	bumps := marketplace.ComputePendingBumps(nil, nil, plugins, fetched)
	if len(bumps) != 0 {
		t.Errorf("pinned plugin should not bump, got %d bumps", len(bumps))
	}
}

func TestComputePendingBumps_ManualMode_NoBump(t *testing.T) {
	plugins := []source.Plugin{
		{
			ID: "manual-plugin",
			Plugin: source.PluginSpec{
				ID:      "manual-plugin@test-mp",
				Version: "1.0.0",
				Update:  "manual",
			},
		},
	}
	fetched := map[string]map[string]marketplace.PluginEntry{
		"test-mp": {
			"manual-plugin": {Name: "manual-plugin", Version: "1.5.0"},
		},
	}

	bumps := marketplace.ComputePendingBumps(nil, nil, plugins, fetched)
	if len(bumps) != 0 {
		t.Errorf("manual plugin should not bump, got %d bumps", len(bumps))
	}
}

func TestComputePendingBumps_DefaultMode_Bumps(t *testing.T) {
	// Update = "" defaults to "track".
	plugins := []source.Plugin{
		{
			ID: "default-plugin",
			Plugin: source.PluginSpec{
				ID:      "default-plugin@test-mp",
				Version: "0.9.0",
				Update:  "",
			},
		},
	}
	fetched := map[string]map[string]marketplace.PluginEntry{
		"test-mp": {
			"default-plugin": {Name: "default-plugin", Version: "1.0.0"},
		},
	}

	bumps := marketplace.ComputePendingBumps(nil, nil, plugins, fetched)
	if len(bumps) != 1 {
		t.Fatalf("default update mode should bump, got %d", len(bumps))
	}
}

func TestComputePendingBumps_AlreadyLatest_NoBump(t *testing.T) {
	plugins := []source.Plugin{
		{
			ID: "up-to-date",
			Plugin: source.PluginSpec{
				ID:      "up-to-date@test-mp",
				Version: "1.0.0",
				Update:  "track",
			},
		},
	}
	fetched := map[string]map[string]marketplace.PluginEntry{
		"test-mp": {
			"up-to-date": {Name: "up-to-date", Version: "1.0.0"},
		},
	}

	bumps := marketplace.ComputePendingBumps(nil, nil, plugins, fetched)
	if len(bumps) != 0 {
		t.Errorf("already-latest plugin should not bump, got %d bumps", len(bumps))
	}
}

func TestComputePendingBumps_NoMarketplace_NoBump(t *testing.T) {
	// Plugin references a marketplace that wasn't fetched.
	plugins := []source.Plugin{
		{
			ID: "orphan",
			Plugin: source.PluginSpec{
				ID:      "orphan@missing-mp",
				Version: "1.0.0",
				Update:  "track",
			},
		},
	}
	fetched := map[string]map[string]marketplace.PluginEntry{} // empty

	bumps := marketplace.ComputePendingBumps(nil, nil, plugins, fetched)
	if len(bumps) != 0 {
		t.Errorf("plugin with no matching marketplace should not bump, got %d", len(bumps))
	}
}
