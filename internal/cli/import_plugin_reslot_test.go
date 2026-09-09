package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportPlugin_SkipsMarketplaceWhoseReslotFails pins the import half of
// #233: `import <agent>:plugin` registers a native marketplace through the same
// addMarketplaceSource as `marketplace add`, so a cache that cannot be
// re-slotted under the declared name must warn and skip that marketplace —
// never register a phantom whose plugin installs then fail. The forcing
// function is the same hostile 300-character declared name (one path segment,
// over every Linux filesystem's 255-byte cap), so the move fails anywhere this
// runs; the assertions name the re-slot error and check that nothing — TOML,
// state, fetch cache — is left behind.
func TestImportPlugin_SkipsMarketplaceWhoseReslotFails(t *testing.T) {
	tmp, env := importTestEnv(t)
	longName := strings.Repeat("n", 300)
	mpDir := writeMarketplaceFixture(t, filepath.Join(t.TempDir(), "hostile-mp"), longName)
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("hostile", mpDir, "demo"))

	out, err := runCLI(t, env, "import", "claude:plugin")
	if err != nil {
		t.Fatalf("import must warn and skip the marketplace, not fail: %v\n%s", err, out)
	}
	for _, want := range []string{"skipping marketplace", "register marketplace", "move marketplace cache"} {
		if !strings.Contains(out, want) {
			t.Fatalf("import must say which marketplace it skipped and why (missing %q); got:\n%s", want, out)
		}
	}
	home := filepath.Join(tmp, ".agentsync")
	if entries, rerr := os.ReadDir(filepath.Join(home, "marketplaces")); rerr == nil && len(entries) != 0 {
		t.Errorf("a marketplace whose cache cannot be re-slotted must not be registered; marketplaces/ holds %d file(s)", len(entries))
	}
	// importTestEnv's `agent add` already wrote targets.json, so check its content.
	if st, rerr := os.ReadFile(filepath.Join(home, ".state", "targets.json")); rerr == nil && strings.Contains(string(st), longName) {
		t.Errorf("a skipped marketplace must record no state entry; targets.json names it:\n%s", st)
	}
	if entries, rerr := os.ReadDir(filepath.Join(home, ".state", "cache", "marketplaces")); rerr == nil && len(entries) != 0 {
		t.Errorf("a skipped marketplace must leave no fetch cache behind; cache root holds %d entr(y/ies)", len(entries))
	}
}
