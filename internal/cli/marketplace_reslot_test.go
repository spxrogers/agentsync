package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// marketplaceCacheNames lists the marketplace cache directories under home.
func marketplaceCacheNames(t *testing.T, tmp string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(tmp, ".agentsync", ".state", "cache", "marketplaces"))
	if err != nil {
		t.Fatalf("read marketplace cache root: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestMarketplaceAdd_CacheIsSlottedUnderTheRegisteredName pins the invariant
// behind #233: `marketplace add` registers the marketplace under its DECLARED
// name (marketplaces/<name>.toml + the state key), and every later lookup —
// resolveMarketplaceEntry, plugin install/upgrade, the poll index — derives the
// cache directory from that same name. So the cache must end up under the
// declared name and NOWHERE else; the slug directory the fetch lands in is
// scratch. A leftover is not cosmetic: searchAllMarketplaces scans every
// directory under the cache root for a bare-id `plugin add`, so an orphan is a
// second, unregistered copy of the marketplace.
func TestMarketplaceAdd_CacheIsSlottedUnderTheRegisteredName(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp, "NO_COLOR": "1"}
	// The fixture path slugs to "…-fixture-mp", which differs from the declared
	// name — the re-slot is exercised, as it is for any real git marketplace.
	fixture := writeMarketplaceFixture(t, filepath.Join(tmp, "fixture-mp"), "test-mp")
	mustRun(t, env, "init")
	mustRun(t, env, "marketplace", "add", fixture)

	if got := marketplaceCacheNames(t, tmp); len(got) != 1 || got[0] != "test-mp" {
		t.Fatalf("the cache must live under the declared name and nothing else; cache dirs = %v, want [test-mp]", got)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "marketplaces", "test-mp.toml")); err != nil {
		t.Fatalf("marketplaces/test-mp.toml must be registered under the same name: %v", err)
	}
	st, err := os.ReadFile(filepath.Join(tmp, ".agentsync", ".state", "targets.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !strings.Contains(string(st), `"test-mp"`) {
		t.Fatalf("the state record must key the marketplace by the same name; got:\n%s", st)
	}
}

// TestMarketplaceAdd_ReAddRefreshesTheCache is the regression for the silent
// half of #233. A re-add re-fetches into the slug directory and re-slots it, but
// os.Rename onto the ALREADY-POPULATED declared-name directory fails with
// ENOTEMPTY. With that discarded, `marketplace add` printed success and wrote a
// fresh head_sha while the cache it points at kept the OLD tree (the fresh one
// orphaned under the slug), so a plugin published since the first add stayed
// invisible forever.
func TestMarketplaceAdd_ReAddRefreshesTheCache(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp, "NO_COLOR": "1"}
	fixture := writeMarketplaceFixture(t, filepath.Join(tmp, "fixture-mp"), "test-mp")
	mustRun(t, env, "init")
	mustRun(t, env, "marketplace", "add", fixture)

	// Upstream publishes a plugin, then the user re-adds the same source.
	const republished = `{"name": "test-mp", "owner": {"name": "x"}, "plugins": [{"name": "demo", "source": "./plugins/demo"}]}`
	mpJSON := filepath.Join(fixture, ".claude-plugin", "marketplace.json")
	if err := os.WriteFile(mpJSON, []byte(republished), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "marketplace", "add", fixture)

	cached, err := os.ReadFile(filepath.Join(tmp, ".agentsync", ".state", "cache", "marketplaces", "test-mp", ".claude-plugin", "marketplace.json"))
	if err != nil {
		t.Fatalf("read the re-slotted cache: %v", err)
	}
	if !strings.Contains(string(cached), `"demo"`) {
		t.Fatalf("a re-add must refresh the cache it registers, not keep the stale tree; cached marketplace.json:\n%s", cached)
	}
	if got := marketplaceCacheNames(t, tmp); len(got) != 1 || got[0] != "test-mp" {
		t.Fatalf("a re-add must leave no orphan slug cache behind; cache dirs = %v, want [test-mp]", got)
	}
}
