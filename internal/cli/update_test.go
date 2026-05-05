package cli_test

import (
	"os"
	"strings"
	"testing"
)

func readFileString(t *testing.T, path string) (string, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	return string(data), err
}

func writeFileString(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o644)
}

// TestUpdate_NoMarketplaces verifies that update with no registered marketplaces
// reports all plugins up to date without error.
func TestUpdate_NoMarketplaces(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "update")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if !strings.Contains(out, "up to date") {
		t.Errorf("expected up-to-date message, got: %s", out)
	}
}

// TestUpdate_FetchesMarketplaceAndReportsUpToDate checks that update fetches
// a local marketplace and reports up-to-date when the plugin version matches.
func TestUpdate_FetchesMarketplaceAndReportsUpToDate(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	mpDir := makeLocalMarketplace(t, t.TempDir())
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}

	// Install a plugin.
	if _, err := runCLI(t, env, "plugin", "install", "demo@test-mp"); err != nil {
		t.Fatal(err)
	}

	// Run update — plugin is already at the version listed in the marketplace
	// (both are "1.0.0"), so no bump should be pending.
	out, err := runCLI(t, env, "update")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	// Should mention marketplace was fetched.
	if !strings.Contains(out, "fetched marketplace") && !strings.Contains(out, "up to date") {
		t.Errorf("expected fetch or up-to-date message, got: %s", out)
	}
}

// TestUpdate_PendingBump verifies that update reports a bump when the marketplace
// has a newer version than what is installed. We directly write the demo.toml
// with an older version and a versioned marketplace fixture to simulate this.
func TestUpdate_PendingBump(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	mpDir := makeVersionedMarketplace(t, t.TempDir(), "2.0.0")
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}

	// Write plugins/demo.toml manually with an older version to simulate an older install.
	home := tmp + "/.agentsync"
	demoTOMLPath := home + "/plugins/demo.toml"
	demoTOML := `[plugin]
id = "demo@test-mp-v"
version = "1.0.0"
update = "track"
agents = ["*"]
`
	if err := os.MkdirAll(home+"/plugins", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileString(t, demoTOMLPath, demoTOML); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "update")
	if err != nil {
		t.Fatalf("update with pending bump: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pending bumps") {
		t.Errorf("expected 'pending bumps' in output, got: %s", out)
	}
	if !strings.Contains(out, "demo") {
		t.Errorf("expected 'demo' plugin in bump output, got: %s", out)
	}
}

// TestUpdate_DryRunNoBump verifies update (no --apply) does NOT modify plugin files.
func TestUpdate_DryRunNoBump(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	mpDir := makeLocalMarketplace(t, t.TempDir())
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "update")
	if err != nil {
		t.Fatalf("update dry run: %v\n%s", err, out)
	}
	// Should NOT say "applied"
	if strings.Contains(out, "applied:") {
		t.Errorf("dry-run update should not apply, got: %s", out)
	}
}
