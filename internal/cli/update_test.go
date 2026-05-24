package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpdate_ApplyRecordsFreshManifestSHA is the regression for the
// update-bricks-plugins bug. computeBump never set Bump.ManifestSHA, and
// applyPluginBump only refreshed the recorded SHA for git fetchers (and even
// then to result.HeadSHA, a git commit SHA — not the sha256(plugin.json)
// that verifyPluginManifestSHA compares). So after a track-mode re-fetch the
// stale pre-bump SHA stayed in plugins/<id>.toml and the immediate re-apply
// hard-failed "manifest SHA mismatch". applyPluginBump must recompute the
// manifest SHA from the freshly-fetched cache, exactly like install.
func TestUpdate_ApplyRecordsFreshManifestSHA(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	// Install demo@1.0.0 (records version + sha256(plugin.json@1.0.0)).
	mpDir := makeVersionedMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "install", "demo@test-mp-v")
	mustRun(t, env, "apply")

	// Publish 1.0.1 in place — plugin.json content changes, so its SHA does too.
	_ = makeVersionedMarketplace(t, base, "1.0.1")

	out, err := runCLI(t, env, "update", "--apply")
	if err != nil {
		t.Fatalf("update --apply bricked the plugin (stale manifest SHA): %v\n%s", err, out)
	}

	demoTOML, rerr := readFileString(t, filepath.Join(tmp, ".agentsync", "plugins", "demo.toml"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(demoTOML, "1.0.1") {
		t.Fatalf("demo.toml not bumped to 1.0.1:\n%s", demoTOML)
	}

	// The recorded SHA must match the new plugin.json, so a follow-up apply
	// doesn't fail verifyPluginManifestSHA.
	if out2, err2 := runCLI(t, env, "apply"); err2 != nil {
		t.Fatalf("apply after update --apply failed (stale SHA recorded?): %v\n%s", err2, out2)
	}
}

// TestUpdate_ApplyBumpFailureLeavesCacheConsistent is the regression for the
// applyPluginBump fetch-then-write window. It overwrote the LIVE plugin cache
// before writing plugins/<id>.toml, so a TOML-write failure left the cache new
// but the recorded version+SHA old. The immediate re-apply's LoadWithCache then
// hard-failed manifest-SHA verification — bricking the WHOLE update, so other
// successfully-bumped plugins never reached the agents. A bump must be
// all-or-nothing: on a write failure the live cache stays untouched and the
// re-apply still proceeds.
func TestUpdate_ApplyBumpFailureLeavesCacheConsistent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	// Install demo@1.0.0 (records version + sha256(plugin.json@1.0.0)).
	mpDir := makeVersionedMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "install", "demo@test-mp-v")
	mustRun(t, env, "apply")

	// Turn plugins/demo.toml into a symlink: applyPluginBump can READ it
	// (os.ReadFile follows the link), but its AtomicWrite refuses a symlink
	// dest — a root-proof way to fail the TOML write AFTER the cache fetch,
	// reproducing the exact inconsistency window.
	home := filepath.Join(tmp, ".agentsync")
	demoTOML := filepath.Join(home, "plugins", "demo.toml")
	realTarget := filepath.Join(base, "demo-real.toml")
	data, err := os.ReadFile(demoTOML)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realTarget, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(demoTOML); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realTarget, demoTOML); err != nil {
		t.Fatal(err)
	}

	// Publish 2.0.0 → a pending forward bump whose TOML write will fail.
	_ = makeVersionedMarketplace(t, base, "2.0.0")

	// The bump fails to write its TOML, but that must NOT brick the re-apply.
	if out, err := runCLI(t, env, "update", "--apply"); err != nil {
		t.Fatalf("update --apply bricked by a half-applied bump (cache/TOML inconsistent): %v\n%s", err, out)
	}
}

// TestUpdate_ApplyPartialFailureRescuesState is the regression for the
// update --apply mirror discarding render.Apply's `written` set and skipping
// the best-effort state save that the real `apply` performs on a mid-pipeline
// error. When the post-bump re-apply fails after some files already landed,
// those files must be recorded so the next apply doesn't reclassify them as
// foreign collisions and needlessly back them up.
func TestUpdate_ApplyPartialFailureRescuesState(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	// Marketplace publishes demo@2.0.0; a manually-pinned demo@1.0.0 → bump.
	mpDir := makeVersionedMarketplace(t, t.TempDir(), "2.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	home := filepath.Join(tmp, ".agentsync")
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileString(t, filepath.Join(home, "plugins", "demo.toml"),
		"[plugin]\nid = \"demo@test-mp-v\"\nversion = \"1.0.0\"\nupdate = \"track\"\nagents = [\"*\"]\n"); err != nil {
		t.Fatal(err)
	}

	// A source MCP server lands in .claude.json during the re-apply (succeeds).
	if err := os.MkdirAll(filepath.Join(home, "mcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileString(t, filepath.Join(home, "mcp", "github.toml"),
		"[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"); err != nil {
		t.Fatal(err)
	}
	// A source skill whose write fails — block the skills dir with a regular file.
	skill := filepath.Join(home, "skills", "demo2", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileString(t, skill, "---\nname: demo2\ndescription: d\n---\nbody\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".claude", "skills"), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "update", "--apply"); err == nil {
		t.Fatal("expected update --apply to fail when the skills dir is blocked")
	}

	st, err := readFileString(t, filepath.Join(home, ".state", "targets.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !strings.Contains(st, "/.claude.json") || !strings.Contains(st, "mcpServers") {
		t.Fatalf("update --apply partial-failure rescue did not record landed .claude.json keys:\n%s", st)
	}
}

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
