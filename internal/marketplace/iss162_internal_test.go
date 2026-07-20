package marketplace

import (
	"os"
	"path/filepath"
	"testing"
)

// writeMarketplaceCacheJSON lays down home/.state/cache/marketplaces/<dir>/
// .claude-plugin/marketplace.json with the given JSON body.
func writeMarketplaceCacheJSON(t *testing.T, home, dir, body string) {
	t.Helper()
	mdir := filepath.Join(home, ".state", "cache", "marketplaces", dir, ".claude-plugin")
	if err := os.MkdirAll(mdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdir, "marketplace.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestResolveInstalledEntry_MemoizedScan is the regression for issue #162 item
// G: resolveInstalledEntry used to re-read and re-parse EVERY cached
// marketplace.json for every plugin (O(plugins × marketplaces)). It now consults
// a marketplace-name → entries index built once per LoadProjected. This test
// asserts (a) memoized resolution matches the direct scan for present entries and
// every fallback (bare id, unknown marketplace, missing entry), and (b) the index
// resolves WITHOUT re-reading — proven by deleting the whole cache and confirming
// the pre-built index still returns the rich entry while the direct scan then
// misses. That the index is built exactly once (never per plugin) is what makes
// each marketplace.json parsed at most once.
func TestResolveInstalledEntry_MemoizedScan(t *testing.T) {
	home := t.TempDir()
	writeMarketplaceCacheJSON(t, home, "a",
		`{"name":"mpA","plugins":[{"name":"plug1","description":"the real plug1"},{"name":"plug2"}]}`)
	writeMarketplaceCacheJSON(t, home, "b",
		`{"name":"mpB","plugins":[{"name":"plug3"}]}`)

	idx := buildMarketplaceIndex(home)

	// Memoized resolution equals the direct scan for present entries and fallbacks.
	checks := []struct {
		id, mp, wantName string
	}{
		{"plug1", "mpA", "plug1"},
		{"plug3", "mpB", "plug3"},
		{"missing", "mpA", "missing"}, // no such entry → bare
		{"plug1", "", "plug1"},        // bare id (no marketplace) → bare
		{"plug1", "mpZ", "plug1"},     // unknown marketplace → bare
	}
	for _, c := range checks {
		memo := resolveInstalledEntry(home, c.id, c.mp, idx)
		scan := resolveInstalledEntry(home, c.id, c.mp, nil)
		if memo.Name.Unverified() != c.wantName {
			t.Errorf("memoized(%q,%q).Name = %q, want %q", c.id, c.mp, memo.Name.Unverified(), c.wantName)
		}
		if memo.Name.Unverified() != scan.Name.Unverified() || memo.Description != scan.Description {
			t.Errorf("memoized vs scan diverged for (%q,%q)", c.id, c.mp)
		}
	}

	// The real plug1 entry carries an inline description; a bare fallback does not.
	if got := resolveInstalledEntry(home, "plug1", "mpA", idx); got.Description != "the real plug1" {
		t.Fatalf("memoized entry lost its inline metadata: %q", got.Description)
	}

	// Delete the whole cache. The pre-built index must STILL resolve the rich
	// entry (zero re-reads), while the direct scan now misses and returns a bare
	// entry (proving the scan path re-reads and the index does not).
	if err := os.RemoveAll(filepath.Join(home, ".state", "cache", "marketplaces")); err != nil {
		t.Fatal(err)
	}
	if got := resolveInstalledEntry(home, "plug1", "mpA", idx); got.Description != "the real plug1" {
		t.Fatalf("memoized index re-read the deleted cache (or lost data): desc=%q", got.Description)
	}
	if got := resolveInstalledEntry(home, "plug1", "mpA", nil); got.Description != "" {
		t.Fatalf("scan should miss after cache deletion, returning a bare entry; got desc=%q", got.Description)
	}
}
