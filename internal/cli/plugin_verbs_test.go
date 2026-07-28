package cli_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// The acceptance suite for the plugin lifecycle verb consolidation (#200 F2):
// the retired top-level `update` → `plugin outdated` / `plugin upgrade --all
// [--lossless]`, with NO forwarding alias (see TestUpdateIsGone below).

// TestPluginOutdated_ReportsPendingBumps is the retired `update`'s read side
// under its new name: it polls, reports the bump, and does NOT touch the pin.
func TestPluginOutdated_ReportsPendingBumps(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mpDir := makeVersionedMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "add", "demo@test-mp-v")
	mustRun(t, env, "apply")

	_ = makeVersionedMarketplace(t, base, "1.0.1")

	out, err := runCLI(t, env, "plugin", "outdated")
	if err != nil {
		t.Fatalf("plugin outdated: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pending bumps") || !strings.Contains(out, "1.0.1") {
		t.Fatalf("plugin outdated did not report the pending bump:\n%s", out)
	}
	// It must point at the NEW upgrade verb, not the retired one.
	if !strings.Contains(out, "plugin upgrade --all") {
		t.Fatalf("plugin outdated should point at `plugin upgrade --all`:\n%s", out)
	}
	// Read-only with respect to the pin.
	demoTOML, _ := readFileString(t, filepath.Join(tmp, ".agentsync", "plugins", "demo.toml"))
	if strings.Contains(demoTOML, "1.0.1") {
		t.Fatalf("plugin outdated must not bump the pin; demo.toml:\n%s", demoTOML)
	}
}

// TestPluginUpgradeAll_UpgradesAndReapplies pins the ratified semantics: the
// consolidation moved `update --apply`'s full re-apply into `plugin upgrade
// --all`, so it is behavior-identical — pin bumped AND agents re-rendered in
// one command.
func TestPluginUpgradeAll_UpgradesAndReapplies(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mpDir := makeVersionedMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "add", "demo@test-mp-v")
	mustRun(t, env, "apply")

	_ = makeVersionedMarketplace(t, base, "1.0.1")

	out, err := runCLI(t, env, "plugin", "upgrade", "--all")
	if err != nil {
		t.Fatalf("plugin upgrade --all: %v\n%s", err, out)
	}
	demoTOML, _ := readFileString(t, filepath.Join(tmp, ".agentsync", "plugins", "demo.toml"))
	if !strings.Contains(demoTOML, "1.0.1") {
		t.Fatalf("plugin upgrade --all did not bump the pin:\n%s", demoTOML)
	}
	if !strings.Contains(out, "applied:") {
		t.Fatalf("plugin upgrade --all must re-apply after the bump; got:\n%s", out)
	}
	// The re-applied render must still verify on a follow-up apply.
	if out2, err2 := runCLI(t, env, "apply"); err2 != nil {
		t.Fatalf("apply after plugin upgrade --all: %v\n%s", err2, out2)
	}
}

// TestPluginUpgradeID_Reapplies pins the deliberate behavior CHANGE: the
// single-id form now finishes with the same re-apply as --all, so one verb has
// one ending state instead of two.
func TestPluginUpgradeID_Reapplies(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mpDir := makeVersionedMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "add", "demo@test-mp-v")
	mustRun(t, env, "apply")

	_ = makeVersionedMarketplace(t, base, "1.0.1")
	// The single-id form re-fetches the PLUGIN, not the marketplace, so refresh
	// the marketplace cache first (unchanged pre-#200 behavior).
	mustRun(t, env, "plugin", "outdated")

	out, err := runCLI(t, env, "plugin", "upgrade", "demo")
	if err != nil {
		t.Fatalf("plugin upgrade demo: %v\n%s", err, out)
	}
	if !strings.Contains(out, "applied:") {
		t.Fatalf("plugin upgrade <id> must re-apply; got:\n%s", out)
	}
	demoTOML, _ := readFileString(t, filepath.Join(tmp, ".agentsync", "plugins", "demo.toml"))
	if !strings.Contains(demoTOML, "1.0.1") {
		t.Fatalf("plugin upgrade <id> did not refresh the pin:\n%s", demoTOML)
	}
}

// TestPluginUpgrade_ArgShapes rejects the two incoherent invocations.
func TestPluginUpgrade_ArgShapes(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")

	if _, err := runCLI(t, env, "plugin", "upgrade"); err == nil {
		t.Fatal("bare `plugin upgrade` should error (needs an id or --all)")
	} else if !strings.Contains(err.Error(), "--all") {
		t.Fatalf("error should point at --all; got: %v", err)
	}
	if _, err := runCLI(t, env, "plugin", "upgrade", "--all", "demo"); err == nil {
		t.Fatal("`plugin upgrade --all <id>` should error")
	}
}

// TestPluginUpgrade_Lossless is the renamed `--auto-safe` (F7), on both forms.
func TestPluginUpgrade_Lossless(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "opencode")

	mpDir := makeLosslessMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "add", "cleanp@as-mp")
	mustRun(t, env, "plugin", "add", "lossyp@as-mp")
	mustRun(t, env, "apply")

	// 2.0.0: lossyp gains an opencode-skipped LSP server; cleanp stays MCP-only.
	_ = makeLosslessMarketplace(t, base, "2.0.0")

	// --lossless without --all is meaningless: it only filters which bumps apply.
	if _, err := runCLI(t, env, "plugin", "outdated", "--lossless"); err == nil {
		t.Fatal("`plugin outdated --lossless` should be rejected (unknown flag)")
	}

	out, err := runCLI(t, env, "plugin", "upgrade", "--all", "--lossless")
	if err != nil {
		t.Fatalf("plugin upgrade --all --lossless: %v\n%s", err, out)
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
	// Excluded bumps are REPORTED, never silently dropped.
	if !strings.Contains(out, "skipping lossy bump lossyp") {
		t.Errorf("lossless must report the excluded bump; got:\n%s", out)
	}

	// The single-id form composes with --lossless too, and refuses the lossy one.
	out, err = runCLI(t, env, "plugin", "upgrade", "lossyp", "--lossless")
	if err != nil {
		t.Fatalf("plugin upgrade lossyp --lossless: %v\n%s", err, out)
	}
	if !strings.Contains(out, "skipping lossy upgrade lossyp") {
		t.Errorf("single-id --lossless must report the refusal; got:\n%s", out)
	}
	lossyTOML, _ = readFileString(t, filepath.Join(home, "plugins", "lossyp.toml"))
	if strings.Contains(lossyTOML, "2.0.0") {
		t.Errorf("single-id --lossless upgraded a lossy plugin anyway:\n%s", lossyTOML)
	}
}

// TestUpdateIsGone pins the ratified alias policy for the LAST command that had
// one. `update` originally kept a deprecated forwarding alias for a minor,
// because the invocation most worth protecting is the cron line `agentsync
// update --apply`. That exception was dropped: this is a pre-1.0 CLI, and "no
// aliases except one" is a worse contract to carry than "no aliases" — the
// whole point of #200 F2/F4/F8 shipping as hard renames.
//
// It must fail like every other retired spelling, on every flag shape a cron
// line could carry, so nobody's automation silently keeps working against a
// command that is on its way out.
//
// The assertion is on the "unknown command" MESSAGE, not merely on a non-zero
// exit. Three of the four shapes below carry flags, and a resurrected `update`
// could reject them for reasons of its own — an unknown `--auto-safe`, a scope
// refusal, a missing arg — leaving the test green while the command resolves
// perfectly well. Only the bare shape is load-bearing under an err != nil
// assertion, and a resurrection as `update <id>` (ExactArgs(1)) passes all four.
func TestUpdateIsGone(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")

	for _, args := range [][]string{
		{"update"},
		{"update", "--apply"},
		{"update", "--apply", "--auto-safe"},
		{"update", "--scope", "user"},
	} {
		out, err := runCLI(t, env, args...)
		switch {
		case err == nil:
			t.Errorf("`agentsync %s` still resolves — `update` is a hard removal, not a deprecation:\n%s",
				strings.Join(args, " "), out)
		case !strings.Contains(err.Error(), "unknown command"):
			t.Errorf("`agentsync %s` failed, but NOT as an unknown command — so this says nothing about "+
				"whether `update` was retired. Got: %v", strings.Join(args, " "), err)
		}
	}
}

// TestExplainTopLevelIsFreed pins F2's deliberate no-alias decision: the
// top-level `explain` no longer takes a plugin id. (What it DOES answer is
// #201's destination-provenance question.)
func TestExplainTopLevelIsFreed(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")

	if _, err := runCLI(t, env, "explain", "somepluginid"); err == nil {
		t.Fatal("top-level `explain <plugin-id>` must no longer resolve to the plugin report")
	}
	// And the plugin report is reachable under its new name.
	out, err := runCLI(t, env, "plugin", "explain", "--all")
	if err != nil {
		t.Fatalf("plugin explain --all: %v\n%s", err, out)
	}
}
