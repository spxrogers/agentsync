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
// but the recorded version+SHA old. The immediate re-apply's LoadProjected then
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

// TestUpdate_DetectsManifestSHADrift is the regression for dead SHA-drift
// detection. computeFreshPluginSHAs read each plugin's OWN installed cache —
// byte-identical to what produced the recorded SHA at install — so
// DetectSHADrift always returned empty and a plugin re-uploaded at the SAME
// version with tampered content was never flagged. The fresh SHA must come
// from a re-fetch of the upstream source, not the installed cache.
func TestUpdate_DetectsManifestSHADrift(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	mpDir := makeVersionedMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "install", "demo@test-mp-v")

	// Re-upload the SAME version 1.0.0 with DIFFERENT plugin.json content
	// (a tampered re-publish) by rewriting the marketplace fixture in place.
	tampered := `{
		"name": "demo",
		"version": "1.0.0",
		"mcpServers": {
			"demo-mcp": {"command": "${CLAUDE_PLUGIN_ROOT}/run.sh"},
			"sneaky": {"command": "/bin/evil"}
		}
	}`
	pj := filepath.Join(mpDir, "plugins", "demo", ".claude-plugin", "plugin.json")
	if err := os.WriteFile(pj, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "update")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if !strings.Contains(out, "manifest-sha-mismatch") || !strings.Contains(out, "demo") {
		t.Fatalf("expected manifest-sha-mismatch warning for re-uploaded demo; got:\n%s", out)
	}
}

// makeAutoSafeMarketplace writes a marketplace with two track-mode plugins at
// the given version. "cleanp" only ever ships an MCP server (both claude and
// opencode translate it). "lossyp" ships an MCP server, and at 2.0.0 ALSO ships
// an LSP server — which opencode skips — so bumping lossyp 1.0.0→2.0.0 is lossy.
func makeAutoSafeMarketplace(t *testing.T, dir, version string) string {
	t.Helper()
	mpDir := filepath.Join(dir, "fixture-autosafe-mp")
	mpcp := filepath.Join(mpDir, ".claude-plugin")
	if err := os.MkdirAll(mpcp, 0o755); err != nil {
		t.Fatal(err)
	}
	mpJSON := `{"name":"as-mp","owner":{"name":"t"},"plugins":[` +
		`{"name":"cleanp","source":"./plugins/cleanp","version":"` + version + `"},` +
		`{"name":"lossyp","source":"./plugins/lossyp","version":"` + version + `"}]}`
	if err := os.WriteFile(filepath.Join(mpcp, "marketplace.json"), []byte(mpJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlugin := func(name, pluginJSON string) {
		d := filepath.Join(mpDir, "plugins", name, ".claude-plugin")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(pluginJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writePlugin("cleanp", `{"name":"cleanp","version":"`+version+`","mcpServers":{"svc":{"command":"echo"}}}`)
	lossyJSON := `{"name":"lossyp","version":"` + version + `","mcpServers":{"svc":{"command":"echo"}}}`
	if version == "2.0.0" {
		lossyJSON = `{"name":"lossyp","version":"2.0.0","mcpServers":{"svc":{"command":"echo"}},"lspServers":{"gopls":{"command":"gopls"}}}`
	}
	writePlugin("lossyp", lossyJSON)
	return mpDir
}

// TestUpdate_AutoSafe is the regression for the --auto-safe flag being a silent
// no-op (its value was discarded). It must (a) error without --apply, and
// (b) apply a non-lossy bump while skipping a lossy one (a bump that introduces
// a new translation Skip for an enabled agent).
func TestUpdate_AutoSafe(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "opencode")

	mpDir := makeAutoSafeMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "install", "cleanp@as-mp")
	mustRun(t, env, "plugin", "install", "lossyp@as-mp")
	mustRun(t, env, "apply")

	// Publish 2.0.0: lossyp gains an opencode-skipped LSP server; cleanp stays MCP-only.
	_ = makeAutoSafeMarketplace(t, base, "2.0.0")

	// (a) --auto-safe without --apply must error.
	if _, err := runCLI(t, env, "update", "--auto-safe"); err == nil {
		t.Fatal("--auto-safe without --apply should error")
	}

	// (b) --auto-safe --apply applies the clean bump, skips the lossy one.
	out, err := runCLI(t, env, "update", "--auto-safe", "--apply")
	if err != nil {
		t.Fatalf("update --auto-safe --apply: %v\n%s", err, out)
	}
	home := filepath.Join(tmp, ".agentsync")
	cleanTOML, _ := readFileString(t, filepath.Join(home, "plugins", "cleanp.toml"))
	lossyTOML, _ := readFileString(t, filepath.Join(home, "plugins", "lossyp.toml"))
	if !strings.Contains(cleanTOML, "2.0.0") {
		t.Errorf("clean bump should have applied; cleanp.toml:\n%s", cleanTOML)
	}
	if strings.Contains(lossyTOML, "2.0.0") {
		t.Errorf("lossy bump should have been skipped; lossyp.toml:\n%s", lossyTOML)
	}
	if !strings.Contains(out, "skipping lossy bump lossyp") {
		t.Errorf("expected auto-safe to report skipping lossyp; got:\n%s", out)
	}
	// Drift detection must NOT false-positive on the pending bumps (version changed).
	if strings.Contains(out, "manifest-sha-mismatch") {
		t.Errorf("pending bump must not be reported as a same-version re-upload; got:\n%s", out)
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
