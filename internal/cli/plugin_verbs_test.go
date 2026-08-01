package cli_test

import (
	"os"
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

// TestLosslessCaveatReachesTheUser is the OUTPUT half of the caveat contract.
//
// TestLosslessTargetingCaveat (internal package) pins what the const must SAY,
// but it reads the const directly — so it stays green even if nothing ever
// prints it. A deletion sweep proved that gap was real: removing the `detail()`
// call in pluginUpgradeRun, removing the once-per-run `Detailf` in
// pollPluginsRun, and gutting `detail()` itself ALL left the suite green. This
// test closes it by asserting on what the commands actually write.
//
// It keys on "plugin explain", one of the tokens the internal test REQUIRES the
// const to contain. That makes the pair load-bearing in both directions: drop
// the const's pointer to `plugin explain` and the internal test fails; stop
// emitting the const and this one does.
func TestLosslessCaveatReachesTheUser(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "opencode")

	// TWO lossy plugins, deliberately. With one, "printed once per run" and
	// "printed once per bump" produce byte-identical output, so the count
	// assertion below would pass either way — a break-verification sweep caught
	// exactly that against the shared single-lossy fixture.
	mpDir := makeTwoLossyMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "add", "lossy1@as2-mp")
	mustRun(t, env, "plugin", "add", "lossy2@as2-mp")
	mustRun(t, env, "apply")
	// 2.0.0: both gain an opencode-skipped LSP server, so both bumps are lossy.
	_ = makeTwoLossyMarketplace(t, base, "2.0.0")

	// The --all form runs FIRST, as in TestPluginUpgrade_Lossless: it is the path
	// that re-fetches the marketplace, and the single-id form below compares
	// against that refreshed cache. Reversed, the single-id run sees no candidate
	// and upgrades happily, which makes the refusal — and this whole test —
	// silently vacuous.
	//
	// It prints the caveat ONCE for the run, not once per bump. Counting is the
	// assertion: a per-bump emission would still contain the substring, so
	// `Contains` alone could not tell the two apart.
	// Streams are checked separately: the caveat is a continuation line hanging
	// under a stderr diagnostic, so landing it on stdout would both detach it
	// from its headline and pollute a redirected result — the same defect the
	// all-excluded line had. Merged output cannot see either.
	stdout, stderr, err := runCLISplit(t, env, "plugin", "upgrade", "--all", "--lossless")
	if err != nil {
		t.Fatalf("plugin upgrade --all --lossless: %v\n%s", err, stderr)
	}
	for _, id := range []string{"lossy1", "lossy2"} {
		if !strings.Contains(stderr, "skipping lossy bump "+id) {
			t.Fatalf("setup: the --all run should have excluded %s; got:\n%s", id, stderr)
		}
	}
	if n := strings.Count(stderr, "plugin explain"); n != 1 {
		t.Errorf("the caveat is a property of the check, not of a bump: want exactly 1 emission "+
			"across 2 lossy bumps, got %d:\n%s", n, stderr)
	}
	if strings.Contains(stdout, "plugin explain") {
		t.Errorf("the caveat is a diagnostic and must not reach stdout; got:\n%s", stdout)
	}

	// The single-id refusal must carry it too: this is the path where a user
	// sees one plugin blocked and has nothing to search for. This is also the
	// ONLY caller of the `detail()` helper, so it is what pins that helper's
	// choice of stream.
	stdout, stderr, err = runCLISplit(t, env, "plugin", "upgrade", "lossy1", "--lossless")
	if err != nil {
		t.Fatalf("plugin upgrade lossy1 --lossless: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "skipping lossy upgrade lossy1") {
		t.Fatalf("setup: the single-id refusal should have fired; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "plugin explain") {
		t.Errorf("the single-id --lossless refusal must carry the targeting caveat; got:\n%s", stderr)
	}
	if strings.Contains(stdout, "plugin explain") {
		t.Errorf("detail() writes a continuation under a stderr diagnostic; on stdout it "+
			"detaches from its headline; got:\n%s", stdout)
	}
}

// TestLossless_UnevaluableIsNotReportedAsLossy pins the partition itself.
//
// --lossless excludes a bump it cannot evaluate, which is right — a fetch or
// parse failure must never let a lossy bump through. But an unevaluable bump
// and a measured one have opposite explanations, and folding them together
// announced "candidate version drops translation for an agent" about a bump
// nothing had judged, then attached the targeting caveat to it. The caveat's
// advice — the loss may fall on an agent this plugin is not projected to,
// re-run without --lossless — is wrong for a bump that was never measured, and
// following it would perform the upgrade the refusal existed to prevent.
func TestLossless_UnevaluableIsNotReportedAsLossy(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "opencode")

	mpDir := makeTwoLossyMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "add", "lossy1@as2-mp")
	mustRun(t, env, "plugin", "add", "lossy2@as2-mp")
	mustRun(t, env, "apply")

	// lossy1's 2.0.0 is genuinely lossy; lossy2's manifest is unparseable, so
	// its bump can be seen but not judged.
	_ = makeTwoLossyMarketplace(t, base, "2.0.0")
	corrupt := filepath.Join(mpDir, "plugins", "lossy2", ".claude-plugin", "plugin.json")
	if err := os.WriteFile(corrupt, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "plugin", "upgrade", "--all", "--lossless")
	if err != nil {
		t.Fatalf("plugin upgrade --all --lossless: %v\n%s", err, out)
	}
	if !strings.Contains(out, "cannot evaluate lossy2") {
		t.Fatalf("setup: lossy2's manifest should have failed evaluation; got:\n%s", out)
	}
	// The measured one keeps the measured wording; the unevaluable one must not
	// borrow it.
	if !strings.Contains(out, "skipping lossy bump lossy1") {
		t.Errorf("the measured bump should still be reported as lossy; got:\n%s", out)
	}
	if strings.Contains(out, "skipping lossy bump lossy2") {
		t.Errorf("an unevaluable bump was never judged lossy; it must not be announced as "+
			"dropping translation; got:\n%s", out)
	}
	if !strings.Contains(out, "skipping bump lossy2") {
		t.Errorf("an excluded bump must still be REPORTED, as what it actually is; got:\n%s", out)
	}
	// ORDER, not just presence. Detailf is an unlabeled continuation line that
	// hangs under whatever precedes it, so a caveat printed after the unevaluable
	// line renders as if it qualified THAT bump — the misattribution the gate
	// exists to prevent. Gating alone cannot catch this; position can.
	caveatAt := strings.Index(out, "plugin explain")
	unevalAt := strings.Index(out, "skipping bump lossy2")
	if caveatAt < 0 || unevalAt < 0 {
		t.Fatalf("setup: expected both the caveat and the unevaluable line; got:\n%s", out)
	}
	if caveatAt > unevalAt {
		t.Errorf("the caveat must hang under the LOSSY line, not the unevaluable one; got:\n%s", out)
	}
	// Neither bump was applied: excluding conservatively is unchanged.
	home := filepath.Join(tmp, ".agentsync")
	for _, id := range []string{"lossy1", "lossy2"} {
		pinned, _ := readFileString(t, filepath.Join(home, "plugins", id+".toml"))
		if strings.Contains(pinned, "2.0.0") {
			t.Errorf("%s should not have been upgraded under --lossless:\n%s", id, pinned)
		}
	}
}

// TestLossless_UnevaluableOnlyRunCarriesNoCaveat is the case the mixed-bump test
// above cannot reach: NOTHING was judged lossy.
//
// With one lossy bump present the caveat prints either way, so a gate on
// `len(lossy)` and a gate on "anything was excluded" are indistinguishable —
// the assertion passes on the bug. Here every exclusion is an evaluation
// failure, so the caveat must be absent entirely. This is the run where its
// advice is actively wrong: "re-run without --lossless" would perform the
// upgrade the refusal exists to prevent, on a bump nobody has measured.
func TestLossless_UnevaluableOnlyRunCarriesNoCaveat(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "opencode")

	mpDir := makeTwoLossyMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "add", "lossy1@as2-mp")
	mustRun(t, env, "plugin", "add", "lossy2@as2-mp")
	mustRun(t, env, "apply")

	// BOTH candidates unparseable: every pending bump is unevaluable.
	_ = makeTwoLossyMarketplace(t, base, "2.0.0")
	for _, id := range []string{"lossy1", "lossy2"} {
		corrupt := filepath.Join(mpDir, "plugins", id, ".claude-plugin", "plugin.json")
		if err := os.WriteFile(corrupt, []byte("{ not json"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runCLI(t, env, "plugin", "upgrade", "--all", "--lossless")
	if err != nil {
		t.Fatalf("plugin upgrade --all --lossless: %v\n%s", err, out)
	}
	if !strings.Contains(out, "cannot evaluate lossy1") || !strings.Contains(out, "cannot evaluate lossy2") {
		t.Fatalf("setup: both candidates should have failed evaluation; got:\n%s", out)
	}
	if strings.Contains(out, "plugin explain") {
		t.Errorf("nothing was judged lossy, so the targeting caveat must not appear — its "+
			"advice would undo a refusal made because nothing is known; got:\n%s", out)
	}
	// And the run must not then claim everything is fine.
	if strings.Contains(out, "all plugins are up to date") {
		t.Errorf("every bump was excluded; reporting success contradicts the refusals; got:\n%s", out)
	}
	if !strings.Contains(out, "excluded all 2 pending bumps") {
		t.Errorf("an all-excluded run must say so; got:\n%s", out)
	}
}

// TestLossless_AllExcludedRunReportsOnStdout pins the terminal outcome of a run
// that applied nothing: which STREAM it lands on, and its singular inflection.
//
// Both were unpinned by the tests above because runCLI merges stdout and
// stderr, and because every other fixture excludes two bumps at once. The
// stream matters: internal/ui states that an informational line which is part
// of the RESULT is not a diagnostic and belongs on Out — the "up to date" line
// this one replaces is a Successf on Out, so routing its sibling to Err would
// split one command's two terminal outcomes across two streams and leave
// `plugin upgrade --all --lossless > log` recording only one of them.
func TestLossless_AllExcludedRunReportsOnStdout(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "opencode")

	// ONLY the lossy plugin, so exactly one bump exists and it is excluded.
	mpDir := makeLosslessMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "add", "lossyp@as-mp")
	mustRun(t, env, "apply")
	_ = makeLosslessMarketplace(t, base, "2.0.0")

	stdout, stderr, err := runCLISplit(t, env, "plugin", "upgrade", "--all", "--lossless")
	if err != nil {
		t.Fatalf("plugin upgrade --all --lossless: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "skipping lossy bump lossyp") {
		t.Fatalf("setup: the only bump should have been excluded; stderr:\n%s", stderr)
	}
	// One excluded bump reads as one, not as "all 1".
	if !strings.Contains(stdout, "excluded the only pending bump") {
		t.Errorf("a single exclusion must read naturally; stdout:\n%s", stdout)
	}
	if strings.Contains(stderr, "no upgrades applied") {
		t.Errorf("the terminal outcome is the command's result and belongs on stdout, "+
			"not among the diagnostics; stderr:\n%s", stderr)
	}
	// And it must never be the contradictory success line.
	if strings.Contains(stdout, "all plugins are up to date") || strings.Contains(stderr, "all plugins are up to date") {
		t.Errorf("every bump was refused; claiming success contradicts that:\n%s\n%s", stdout, stderr)
	}
}

// makeTwoLossyMarketplace is makeLosslessMarketplace with TWO lossy plugins and
// no clean one. It exists so a "printed once per run" assertion can be made by
// COUNTING: against a single-lossy fixture, once-per-run and once-per-bump emit
// the same bytes and the count cannot tell them apart.
//
// Both plugins ship an MCP server at every version and ALSO an LSP server at
// 2.0.0 — which opencode skips — so both 1.0.0→2.0.0 bumps are lossy.
func makeTwoLossyMarketplace(t *testing.T, dir, version string) string {
	t.Helper()
	mpDir := filepath.Join(dir, "fixture-twolossy-mp")
	mpcp := filepath.Join(mpDir, ".claude-plugin")
	if err := os.MkdirAll(mpcp, 0o755); err != nil {
		t.Fatal(err)
	}
	mpJSON := `{"name":"as2-mp","owner":{"name":"t"},"plugins":[` +
		`{"name":"lossy1","source":"./plugins/lossy1","version":"` + version + `"},` +
		`{"name":"lossy2","source":"./plugins/lossy2","version":"` + version + `"}]}`
	if err := os.WriteFile(filepath.Join(mpcp, "marketplace.json"), []byte(mpJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lossy1", "lossy2"} {
		pluginJSON := `{"name":"` + name + `","version":"` + version + `","mcpServers":{"svc":{"command":"echo"}}}`
		if version == "2.0.0" {
			pluginJSON = `{"name":"` + name + `","version":"2.0.0","mcpServers":{"svc":{"command":"echo"}},` +
				`"lspServers":{"gopls":{"command":"gopls"}}}`
		}
		d := filepath.Join(mpDir, "plugins", name, ".claude-plugin")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(pluginJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return mpDir
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
