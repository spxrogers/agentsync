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

// TestComputePendingBumps_TrackMode_NoDowngrade is the regression for
// computeBump using raw string inequality: a marketplace that rolls back to an
// OLDER version was reported (and auto-applied) as a "bump" — a silent
// downgrade. Track mode must only bump strictly forward.
func TestComputePendingBumps_TrackMode_NoDowngrade(t *testing.T) {
	plugins := []source.Plugin{
		{ID: "demo", Plugin: source.PluginSpec{ID: "demo@test-mp", Version: "2.0.0", Update: "track"}},
	}
	fetched := map[string]map[string]marketplace.PluginEntry{
		"test-mp": {"demo": {Name: "demo", Version: "1.5.0"}},
	}
	if bumps := marketplace.ComputePendingBumps(nil, nil, plugins, fetched); len(bumps) != 0 {
		t.Fatalf("expected no bump for a rollback/downgrade, got %d: %+v", len(bumps), bumps)
	}
}

// TestComputePendingBumps_NonSemverTracksLatest verifies that when versions
// are not comparable as semver, track mode still follows any change so an
// exotic versioning scheme isn't stranded on its first version.
func TestComputePendingBumps_NonSemverTracksLatest(t *testing.T) {
	plugins := []source.Plugin{
		{ID: "demo", Plugin: source.PluginSpec{ID: "demo@test-mp", Version: "stable", Update: "track"}},
	}
	fetched := map[string]map[string]marketplace.PluginEntry{
		"test-mp": {"demo": {Name: "demo", Version: "edge"}},
	}
	if bumps := marketplace.ComputePendingBumps(nil, nil, plugins, fetched); len(bumps) != 1 {
		t.Fatalf("expected track-latest fallback to bump on non-semver change, got %d", len(bumps))
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

// ---- SHA drift detection tests -----------------------------------------------

func TestDetectSHADrift_NoDrift(t *testing.T) {
	plugins := []source.Plugin{
		{
			ID: "demo",
			Plugin: source.PluginSpec{
				ID:          "demo@test-mp",
				Version:     "1.0.0",
				ManifestSHA: "abc123",
			},
		},
	}
	// Same SHA → no warning.
	warnings := marketplace.DetectSHADrift(plugins, map[string]string{"demo": "abc123"})
	if len(warnings) != 0 {
		t.Errorf("expected no warnings when SHAs match, got %d", len(warnings))
	}
}

func TestDetectSHADrift_Drift(t *testing.T) {
	plugins := []source.Plugin{
		{
			ID: "demo",
			Plugin: source.PluginSpec{
				ID:          "demo@test-mp",
				Version:     "1.0.0",
				ManifestSHA: "oldsha",
			},
		},
	}
	// Different SHA → warning.
	warnings := marketplace.DetectSHADrift(plugins, map[string]string{"demo": "newsha"})
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
	w := warnings[0]
	if w.ID != "demo" {
		t.Errorf("warning ID = %q, want demo", w.ID)
	}
	if w.RecordedSHA != "oldsha" {
		t.Errorf("RecordedSHA = %q, want oldsha", w.RecordedSHA)
	}
	if w.FetchedSHA != "newsha" {
		t.Errorf("FetchedSHA = %q, want newsha", w.FetchedSHA)
	}
	if w.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", w.Version)
	}
}

func TestDetectSHADrift_NoRecordedSHA(t *testing.T) {
	// If plugin has no recorded SHA, no warning (it may not have been installed with pinning).
	plugins := []source.Plugin{
		{
			ID: "demo",
			Plugin: source.PluginSpec{
				ID:          "demo@test-mp",
				Version:     "1.0.0",
				ManifestSHA: "",
			},
		},
	}
	warnings := marketplace.DetectSHADrift(plugins, map[string]string{"demo": "newsha"})
	if len(warnings) != 0 {
		t.Errorf("expected no warnings when no recorded SHA, got %d", len(warnings))
	}
}

func TestDetectSHADrift_NoFreshSHA(t *testing.T) {
	// If fresh SHA is not available (plugin not cached), no warning.
	plugins := []source.Plugin{
		{
			ID: "demo",
			Plugin: source.PluginSpec{
				ID:          "demo@test-mp",
				Version:     "1.0.0",
				ManifestSHA: "abc123",
			},
		},
	}
	warnings := marketplace.DetectSHADrift(plugins, map[string]string{}) // no fresh SHA
	if len(warnings) != 0 {
		t.Errorf("expected no warnings when no fresh SHA available, got %d", len(warnings))
	}
}
