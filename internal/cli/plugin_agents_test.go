package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeFanOutMarketplace builds a local marketplace with ONE plugin that ships
// every component kind whose duplication the `native_agents` deferral exists to
// prevent: a subagent, a skill, a command, an MCP server, and a hook.
//
// Each kind has a distinct failure mode when it lands twice — two agents in the
// picker, two entries in the skill list, one server id registered from two
// sources, and (the one that actually misbehaves rather than just cluttering) a
// hook handler that fires twice per event.
func makeFanOutMarketplace(t *testing.T, dir string) string {
	t.Helper()
	mpDir := filepath.Join(dir, "fanout-marketplace")

	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(mpDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(".claude-plugin/marketplace.json", `{
		"name": "fanout-mp",
		"owner": {"name": "tester"},
		"plugins": [{"name": "toolkit", "source": "./plugins/toolkit"}]
	}`)
	write("plugins/toolkit/.claude-plugin/plugin.json", `{"name":"toolkit","version":"1.0.0"}`)
	write("plugins/toolkit/agents/reviewer.md",
		"---\nname: reviewer\ndescription: review code\n---\nReview the diff.\n")
	write("plugins/toolkit/skills/audit/SKILL.md",
		"---\nname: audit\ndescription: audit code\n---\nAudit the tree.\n")
	write("plugins/toolkit/commands/scan.md",
		"---\ndescription: scan\n---\nScan the repo.\n")
	write("plugins/toolkit/.mcp.json", `{"mcpServers":{"toolkit-srv":{"command":"toolkit-server"}}}`)
	write("plugins/toolkit/hooks/hooks.json", `{
		"hooks": {"PreToolUse": [{"matcher": "Write", "hooks": [{"type":"command","command":"toolkit-guard"}]}]}
	}`)
	return mpDir
}

// claudeToolkitPaths are the destination files Claude's projection of the
// toolkit plugin writes. Each is namespaced by the plugin (issue #211), so they
// never collide with the plugin's own copies — which is exactly why they
// DUPLICATE them instead: Claude keeps serving `toolkit:reviewer` from its
// install dir while these render as `toolkit-reviewer`.
func claudeToolkitPaths(tmp string) map[string]string {
	return map[string]string{
		"subagent": filepath.Join(tmp, ".claude", "agents", "toolkit-reviewer.md"),
		"skill":    filepath.Join(tmp, ".claude", "skills", "toolkit-audit", "SKILL.md"),
		"command":  filepath.Join(tmp, ".claude", "commands", "toolkit-scan.md"),
	}
}

func mustExist(t *testing.T, label, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected the %s at %s: %v", label, path, err)
	}
}

func mustNotExist(t *testing.T, label, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("%s must not be projected to this agent; found %s", label, path)
	}
}

// mustNotMention asserts a shared destination file does not carry needle. A
// MISSING file satisfies it — for a component rendered into a shared JSON file
// (an MCP server in .claude.json, a hook in settings.json) "the file was never
// created" and "the file exists without this key" are the same outcome — but the
// check is explicit about that rather than skipping silently when the file is
// absent, which would make the assertion vacuous exactly when the render path
// changed.
// pinPath is the canonical pin for a plugin under an agentsync home.
func pinPath(tmp, plugin string) string {
	return filepath.Join(tmp, ".agentsync", "plugins", plugin+".toml")
}

// deferInPin records `native_agents = [<agents>]` in a plugin's pin — what the
// user does by hand, and what `import` does for them. It APPENDS rather than
// rewriting an existing key, so callers must not use it on a pin that already
// has one; adoptInPin is the inverse.
//
// Both exist because the read-edit-write block was copy-pasted across eight
// call sites, every one hard-coding go-toml's single-quote rendering. A change
// in quoting style would have silently turned each `ReplaceAll` into a no-op —
// failing safe, but leaving tests that assert nothing.
func deferInPin(t *testing.T, tmp, plugin string, agents ...string) {
	t.Helper()
	quoted := make([]string, len(agents))
	for i, a := range agents {
		quoted[i] = "'" + a + "'"
	}
	body := mustReadFile(t, pinPath(tmp, plugin)) +
		"\nnative_agents = [" + strings.Join(quoted, ", ") + "]\n"
	if err := os.WriteFile(pinPath(tmp, plugin), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// adoptInPin removes the `native_agents` key from a plugin's pin — what a user
// does after uninstalling the plugin inside the agent. It asserts the key was
// actually there, so a quoting or formatting change cannot turn this into a
// silent no-op that leaves the test asserting nothing.
func adoptInPin(t *testing.T, tmp, plugin string) {
	t.Helper()
	before := mustReadFile(t, pinPath(tmp, plugin))
	var kept []string
	removed := false
	for _, line := range strings.Split(before, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "native_agents") {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		t.Fatalf("adoptInPin found no native_agents key to remove in:\n%s", before)
	}
	if err := os.WriteFile(pinPath(tmp, plugin), []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustNotMention(t *testing.T, label, path, needle string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return // nothing was written here at all
		}
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(data), needle) {
		t.Errorf("%s must not be projected to this agent; %s contains %q:\n%s", label, path, needle, data)
	}
}

// mustReadFile is readFileString's fatal-on-error sibling: these tests always
// treat a missing file as a test failure, never as a condition to branch on.
func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestImportApply_NativePluginIsNotDuplicated is the end-to-end regression for
// the reported flow: `agentsync import claude` followed by `agentsync apply`,
// with the plugin still enabled in Claude's own settings.
//
// Before the deferral, apply projected the plugin's components into Claude's
// standalone paths while Claude kept loading its own copies from its install
// dir — two of every skill, subagent, and command, and a hook handler that fired
// twice. apply never disables the native plugin (PluginIngester is read-only by
// design), so the duplicate could only be avoided by not projecting there.
//
// The oracle is the ON-DISK RESULT: Claude's destination must hold none of the
// plugin's projected components, while codex — which does NOT have the plugin
// natively — must hold all of them, proving the deferral is per-agent and does
// not disable the plugin outright.
func TestImportApply_NativePluginIsNotDuplicated(t *testing.T) {
	tmp, env := importTestEnv(t) // inits + enables claude
	if _, err := runCLI(t, env, "agent", "add", "codex"); err != nil {
		t.Fatalf("agent add codex: %v", err)
	}
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("fanout-mp", mpDir, "toolkit"))

	out, err := runCLI(t, env, "import", "claude:plugin")
	if err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	// The deferral is reported, not silent: it changes what the next apply
	// writes, so the user has to be able to see it happen.
	if !strings.Contains(out, "native_agents=[claude]") {
		t.Errorf("import should report the seeded deferral; got:\n%s", out)
	}

	// It is recorded in canonical state — apply reads THIS, never the
	// destination, so the plan stays reproducible from the dotfiles repo alone.
	toml := mustReadFile(t, filepath.Join(tmp, ".agentsync", "plugins", "toolkit.toml"))
	if !strings.Contains(toml, `native_agents = ['claude']`) {
		t.Errorf("plugins/toolkit.toml should record the deferral; got:\n%s", toml)
	}
	// The user's own fan-out allowlist is untouched — the deferral is a separate
	// statement about the world, not a narrowing of their choice.
	if !strings.Contains(toml, `agents = ['*']`) {
		t.Errorf("the `agents` allowlist must stay ['*']; got:\n%s", toml)
	}

	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	// Claude serves every one of these itself; agentsync must add nothing.
	for label, path := range claudeToolkitPaths(tmp) {
		mustNotExist(t, label, path)
	}
	// The MCP server and the hook are the two that misbehave rather than merely
	// clutter — a doubly-registered server id, and a handler that fires twice.
	// Both live inside shared JSON files, so "absent" means either the file does
	// not exist or it does not mention the component; mustNotMention states that
	// rather than leaving it to an `err == nil` conditional that silently skips
	// the assertion when the file happens not to exist.
	mustNotMention(t, "the plugin's MCP server", filepath.Join(tmp, ".claude.json"), "toolkit-srv")
	mustNotMention(t, "the plugin's hook (it would fire twice)",
		filepath.Join(tmp, ".claude", "settings.json"), "toolkit-guard")

	// Codex does not have the plugin natively, so it gets the full fan-out —
	// which is the entire point of importing the plugin in the first place.
	mustExist(t, "codex subagent", filepath.Join(tmp, ".codex", "agents", "toolkit-reviewer.toml"))
	// Codex reads personal skills from the shared ~/.agents/skills dir, not from
	// under ~/.codex (see internal/adapter/codex/paths.go).
	mustExist(t, "codex skill", filepath.Join(tmp, ".agents", "skills", "toolkit-audit", "SKILL.md"))
	if data := mustReadFile(t, filepath.Join(tmp, ".codex", "config.toml")); !strings.Contains(data, "toolkit-srv") {
		t.Errorf("codex should receive the plugin's MCP server; got:\n%s", data)
	}
}

// TestApply_AdoptingANativePluginProjectsItAgain proves the deferral is
// reversible and that reversing it is a canonical-source edit, not a hidden
// destination probe: dropping the agent from `native_agents` (what a user does
// after `/plugin uninstall` in Claude) makes the next apply project the
// components there, with no re-import and no change to Claude's own config.
func TestApply_AdoptingANativePluginProjectsItAgain(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("fanout-mp", mpDir, "toolkit"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	for label, path := range claudeToolkitPaths(tmp) {
		mustNotExist(t, label, path)
	}

	// The user uninstalls the plugin in Claude and adopts it into agentsync by
	// dropping the deferral.
	adoptInPin(t, tmp, "toolkit")
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply after adopting: %v\n%s", err, out)
	}
	for label, path := range claudeToolkitPaths(tmp) {
		mustExist(t, label, path)
	}
}

// TestApply_DeferralReclaimsPreviouslyProjectedFiles covers the upgrade path in
// the other direction: a user who already applied (so the duplicates are on
// disk) and then records the deferral must have those files REMOVED, not
// orphaned. Leaving them would mean the fix adds nothing — the duplicate stays,
// it just stops being refreshed.
//
// This is the ordinary orphan-reclamation path doing its job: a component that
// stops rendering leaves no op, and apply reclaims the file it owns in state.
func TestApply_DeferralReclaimsPreviouslyProjectedFiles(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	// No native marketplace/plugin in Claude's settings: register with agentsync
	// directly so the first import seeds NO deferral and apply projects to claude.
	if out, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "plugin", "add", "toolkit"); err != nil {
		t.Fatalf("plugin add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	for label, path := range claudeToolkitPaths(tmp) {
		mustExist(t, label, path)
	}

	// Now the user installs the plugin in Claude natively and records it.
	deferInPin(t, tmp, "toolkit", "claude")
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply after deferring: %v\n%s", err, out)
	}
	for label, path := range claudeToolkitPaths(tmp) {
		mustNotExist(t, label, path)
	}
	// The key-merge kinds are reclaimed by a DIFFERENT path (orphanCleanupOps,
	// not orphanDeletes), and pipeline.go claims both are handled. The fixture
	// ships an MCP server and a hook precisely because those are the two that
	// misbehave when duplicated, so assert them here rather than trusting the
	// claim — per "point to the test that backs it".
	mustNotMention(t, "the plugin's MCP server", filepath.Join(tmp, ".claude.json"), "toolkit-srv")
	mustNotMention(t, "the plugin's hook", filepath.Join(tmp, ".claude", "settings.json"), "toolkit-guard")
}

// TestApply_PluginAgentsAllowlistNarrowsFanOut covers the OTHER gate — the
// `agents` allowlist that docs have advertised since v1.0 while nothing read it
// (PluginSpec.Agents was written and preserved but never consulted, so a
// narrowed allowlist silently fanned out to every agent anyway).
func TestApply_PluginAgentsAllowlistNarrowsFanOut(t *testing.T) {
	tmp, env := importTestEnv(t)
	if _, err := runCLI(t, env, "agent", "add", "codex"); err != nil {
		t.Fatalf("agent add codex: %v", err)
	}
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	if out, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "plugin", "add", "toolkit"); err != nil {
		t.Fatalf("plugin add: %v\n%s", err, out)
	}

	tomlPath := filepath.Join(tmp, ".agentsync", "plugins", "toolkit.toml")
	narrowed := strings.ReplaceAll(mustReadFile(t, tomlPath), "agents = ['*']", "agents = ['codex']")
	if err := os.WriteFile(tomlPath, []byte(narrowed), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	for label, path := range claudeToolkitPaths(tmp) {
		mustNotExist(t, label, path)
	}
	mustExist(t, "codex subagent", filepath.Join(tmp, ".codex", "agents", "toolkit-reviewer.toml"))
}

// TestApply_HandAuthoredComponentsIgnoreTheAllowlist pins the boundary: the two
// gates are properties of PLUGIN installation, so a component the user wrote
// into ~/.agentsync/ renders everywhere regardless of any plugin's targeting.
// Without this, a filter keyed on the wrong field would quietly stop rendering
// the user's own components.
func TestApply_HandAuthoredComponentsIgnoreTheAllowlist(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("fanout-mp", mpDir, "toolkit"))
	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}

	// A hand-authored subagent, alongside a plugin deferred away from claude.
	dir := filepath.Join(tmp, ".agentsync", "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mine.md"),
		[]byte("---\nname: mine\ndescription: mine\n---\nMy own agent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	mustExist(t, "hand-authored subagent", filepath.Join(tmp, ".claude", "agents", "mine.md"))
	mustNotExist(t, "plugin subagent", claudeToolkitPaths(tmp)["subagent"])
}

// TestStatus_WarnsWhenAgentAlsoInstallsThePluginItself covers the case apply
// structurally cannot: a plugin declared in agentsync (and projected to claude)
// that the user LATER installs in Claude natively. apply's plan is a pure
// function of canonical state and never probes the destination — deliberately,
// so the same source renders the same way everywhere — which means the
// resulting duplicate can only be noticed by a command that does read the
// destination. status and doctor are those commands.
func TestStatus_WarnsWhenAgentAlsoInstallsThePluginItself(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	if out, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "plugin", "add", "toolkit"); err != nil {
		t.Fatalf("plugin add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// Quiet while agentsync is the only source of the components.
	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if strings.Contains(out, "projected there by agentsync") {
		t.Errorf("status must not warn before the plugin is installed natively; got:\n%s", out)
	}

	// The user now installs the same plugin inside Claude Code.
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("fanout-mp", mpDir, "toolkit"))
	out, err = runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "projected there by agentsync") || !strings.Contains(out, "native_agents") {
		t.Errorf("status should warn about the duplicate and name the remedy; got:\n%s", out)
	}

	// Recording the deferral resolves it — and the warning goes away without any
	// change to Claude's own config.
	deferInPin(t, tmp, "toolkit", "claude")
	out, err = runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if strings.Contains(out, "projected there by agentsync") {
		t.Errorf("recording the deferral should clear the warning; got:\n%s", out)
	}
}

// TestImport_RefusesLeftoverProjectedFilesAfterDeferral is the regression for the
// window between recording a deferral and the apply that reclaims the files.
//
// import's capture-refusal filter is deliberately NOT narrowed to the agent's
// own render set. Narrowing it reads as correct — a plugin that projects nothing
// here provides nothing here — and is wrong: for as long as the reclaiming apply
// has not run, the destination still holds agentsync's OWN rendered output, and a
// narrowed filter captures that back into ~/.agentsync as if the user had
// written it. Those copies then diverge from the plugin's on its next update,
// permanently, with no signal.
//
// The oracle is the canonical source: nothing plugin-provided may appear there.
func TestImport_RefusesLeftoverProjectedFilesAfterDeferral(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	if out, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "plugin", "add", "toolkit"); err != nil {
		t.Fatalf("plugin add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// Positive control: the files really are on disk, so the refusal below is
	// about a real capture opportunity rather than an empty directory.
	for label, path := range claudeToolkitPaths(tmp) {
		mustExist(t, label, path)
	}

	// Record the deferral (what `import claude:plugin` does) and do NOT apply —
	// this is the window.
	deferInPin(t, tmp, "toolkit", "claude")

	for _, component := range []string{"subagent", "skill", "command"} {
		out, err := runCLI(t, env, "import", "claude:"+component)
		if err != nil {
			t.Fatalf("import claude:%s: %v\n%s", component, err, out)
		}
		if !strings.Contains(out, "it is projected from the plugin") {
			t.Errorf("import claude:%s must still refuse the plugin's own output; got:\n%s", component, out)
		}
	}
	for _, rel := range []string{
		filepath.Join("subagents", "toolkit-reviewer.md"),
		filepath.Join("skills", "toolkit-audit", "SKILL.md"),
		filepath.Join("commands", "toolkit-scan.md"),
	} {
		if _, err := os.Stat(filepath.Join(tmp, ".agentsync", rel)); err == nil {
			t.Errorf("import captured agentsync's own projected output as hand-authored: %s", rel)
		}
	}
}

// TestImport_DryRunPreviewsTheQuestionRatherThanTheAnswer pins the call site of
// the preview note. Round 2 tested the note builder in isolation; replacing the
// call with "" stayed green, so the wiring itself was unpinned — and a preview
// that silently says nothing is exactly as wrong as one that over-promises.
func TestImport_DryRunPreviewsTheQuestionRatherThanTheAnswer(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("fanout-mp", mpDir, "toolkit"))

	out, err := runCLI(t, env, "import", "claude:plugin", "--dry-run")
	if err != nil {
		t.Fatalf("import --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "will ask") {
		t.Errorf("the preview must say the real run asks; got:\n%s", out)
	}
	if strings.Contains(out, "native_agents=[claude]") {
		t.Errorf("the preview asserted an outcome the user may decline; got:\n%s", out)
	}
	// A dry run writes nothing at all.
	if _, err := os.Stat(pinPath(tmp, "toolkit")); err == nil {
		t.Error("--dry-run wrote a plugin pin")
	}
}

// TestImport_DoesNotReAddADeferralAfterAdoption pins the adoption path end to
// end. Adopting a plugin means uninstalling it inside the agent and dropping the
// `native_agents` entry; the next import must not quietly put it back, which
// would stop projecting to an agent the user deliberately took over — and under
// a non-TTY stdin it would do so without even asking.
func TestImport_DoesNotReAddADeferralAfterAdoption(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("fanout-mp", mpDir, "toolkit"))
	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	tomlPath := filepath.Join(tmp, ".agentsync", "plugins", "toolkit.toml")
	if !strings.Contains(mustReadFile(t, tomlPath), "native_agents = ['claude']") {
		t.Fatalf("setup: the first import should have recorded the deferral:\n%s", mustReadFile(t, tomlPath))
	}

	// Adopt: uninstall the plugin inside Claude, and drop the deferral.
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("fanout-mp", mpDir))
	adoptInPin(t, tmp, "toolkit")

	// A re-import must not resurrect it: Claude no longer installs the plugin,
	// so there is nothing to defer to.
	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	if got := mustReadFile(t, tomlPath); strings.Contains(got, "native_agents") {
		t.Errorf("a re-import re-added a deferral the user adopted away:\n%s", got)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	for label, path := range claudeToolkitPaths(tmp) {
		mustExist(t, label, path)
	}
}

// TestImport_PreservesAnExplicitlyEmptyDeferral pins the "defer to nobody, stop
// asking" escape hatch across a full import cycle. `native_agents = []` is the
// user saying they want the plugin projected everywhere INCLUDING agents that
// install it themselves — a duplicate they chose. A re-import must preserve
// that: before the field became a pointer, `omitempty` erased the empty list on
// the rewrite and the next import silently re-seeded the deferral instead.
func TestImport_PreservesAnExplicitlyEmptyDeferral(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("fanout-mp", mpDir, "toolkit"))
	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	tomlPath := filepath.Join(tmp, ".agentsync", "plugins", "toolkit.toml")
	emptied := strings.ReplaceAll(mustReadFile(t, tomlPath),
		"native_agents = ['claude']", "native_agents = []")
	if err := os.WriteFile(tomlPath, []byte(emptied), 0o644); err != nil {
		t.Fatal(err)
	}

	// Claude still installs the plugin, so a re-import has every reason to
	// re-seed — and must not, because the empty list is a recorded decision.
	out, err := runCLI(t, env, "import", "claude:plugin")
	if err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	// The item line reports what was WRITTEN, not what was a candidate. Reporting
	// the candidates here would claim `native_agents=[claude]` for an import that
	// deliberately wrote nothing of the sort — the same misreport class that made
	// `plugin explain` call a deferred plugin a failure.
	if strings.Contains(out, "native_agents=[claude]") {
		t.Errorf("the item line claimed a deferral the import did not write; got:\n%s", out)
	}
	got := mustReadFile(t, tomlPath)
	if !strings.Contains(got, "native_agents = []") {
		t.Fatalf("the explicit 'defer to nobody' decision did not survive a re-import:\n%s", got)
	}
	// And it means what it says: apply projects to claude despite the native copy.
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	for label, path := range claudeToolkitPaths(tmp) {
		mustExist(t, label, path)
	}
}

// TestDoctor_WarnsWhenAgentAlsoInstallsThePluginItself mirrors the status test on
// the other command that reads native state. Both surfaces matter: `doctor` is
// where a user looks when something is wrong with the machine, and it was the
// half whose warning no test covered.
func TestDoctor_WarnsWhenAgentAlsoInstallsThePluginItself(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	if out, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "plugin", "add", "toolkit"); err != nil {
		t.Fatalf("plugin add: %v\n%s", err, out)
	}
	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if strings.Contains(out, "also projected by agentsync") {
		t.Errorf("doctor must not warn before the plugin is installed natively; got:\n%s", out)
	}

	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("fanout-mp", mpDir, "toolkit"))
	out, err = runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "also projected by agentsync") || !strings.Contains(out, "native_agents") {
		t.Errorf("doctor should report the duplicate and name the remedy; got:\n%s", out)
	}

	// Recording the deferral resolves it. Without this phase the test would pass
	// for a doctor that warned unconditionally once any plugin was declared.
	deferInPin(t, tmp, "toolkit", "claude")
	out, err = runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if strings.Contains(out, "also projected by agentsync") {
		t.Errorf("recording the deferral should clear doctor's warning; got:\n%s", out)
	}
}

// TestPluginExplain_ReportsDeferralRatherThanFailure pins the OTHER renderer of
// a translation-report row. `plugin explain` has its own emit path, and it fell
// through to the generic failure mark: a plugin the user had deliberately
// deferred printed as a red "✗ native  no components" — indistinguishable from
// an adapter that could not translate anything, and precisely the "why did this
// render nothing?" confusion the row exists to answer.
//
// The wording lives on render.PluginRow.TargetingNote so the two renderers
// cannot drift; this test is the end-to-end proof that the explain path uses it.
func TestPluginExplain_ReportsDeferralRatherThanFailure(t *testing.T) {
	tmp, env := importTestEnv(t)
	if _, err := runCLI(t, env, "agent", "add", "codex"); err != nil {
		t.Fatalf("agent add codex: %v", err)
	}
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("fanout-mp", mpDir, "toolkit"))
	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}

	out, err := runCLI(t, env, "plugin", "explain", "toolkit")
	if err != nil {
		t.Fatalf("plugin explain: %v\n%s", err, out)
	}
	if !strings.Contains(out, "served natively") {
		t.Errorf("explain should say WHY claude gets nothing; got:\n%s", out)
	}
	// The failure vocabulary must not appear for a deliberate deferral. "✗" is
	// the adapter-could-not-translate mark; using it here is the bug.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "claude") && strings.Contains(line, "✗") {
			t.Errorf("a deferred agent was reported as a failure: %q", line)
		}
	}
	// Codex still receives the plugin, so its row is an ordinary coverage row —
	// the positive control that explain did not simply stop reporting.
	if !strings.Contains(out, "codex") {
		t.Errorf("explain should still report the agents that DO receive the plugin; got:\n%s", out)
	}

	// The --json contract carries the machine-readable form of the same fact.
	jsonOut, err := runCLI(t, env, "plugin", "explain", "toolkit", "--json")
	if err != nil {
		t.Fatalf("plugin explain --json: %v\n%s", err, jsonOut)
	}
	if !strings.Contains(jsonOut, `"notTargeted": true`) || !strings.Contains(jsonOut, `"coverage": "native"`) {
		t.Errorf("--json should expose notTargeted + the native coverage value; got:\n%s", jsonOut)
	}
}

// TestProjectScope_NativeAgentsIsHonoured pins the deferral at PROJECT scope.
// Project scope renders from the project-only overlay rather than the merged
// canonical, so it reaches the filter by a different path than the user-scope
// tests above — and a pin committed into a repo carries its `native_agents` with
// it, which is the intended behavior: committing the pin commits the statement
// that this agent serves the plugin natively.
func TestProjectScope_NativeAgentsIsHonoured(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	for _, args := range [][]string{
		{"init"},
		{"agent", "add", "claude"},
		{"init", "--scope", "project", "--project", proj},
		{"agent", "add", "claude", "--scope", "project", "--project", proj},
	} {
		if out, err := runCLI(t, env, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	// Acquire the plugin at user scope, then commit its pin into the project.
	mpDir := makeFanOutMarketplace(t, t.TempDir())
	if out, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "plugin", "add", "toolkit"); err != nil {
		t.Fatalf("plugin add: %v\n%s", err, out)
	}
	pin := mustReadFile(t, filepath.Join(tmpHome, ".agentsync", "plugins", "toolkit.toml"))
	projPlugins := filepath.Join(proj, ".agentsync", "plugins")
	if err := os.MkdirAll(projPlugins, 0o755); err != nil {
		t.Fatal(err)
	}
	projPin := filepath.Join(projPlugins, "toolkit.toml")
	if err := os.WriteFile(projPin, []byte(pin), 0o644); err != nil {
		t.Fatal(err)
	}

	// Positive control: with no deferral the project apply projects to claude.
	if out, err := runCLI(t, env, "apply", "--scope", "project", "--project", proj); err != nil {
		t.Fatalf("project apply: %v\n%s", err, out)
	}
	projected := filepath.Join(proj, ".claude", "agents", "toolkit-reviewer.md")
	mustExist(t, "project-scope plugin subagent", projected)

	// Now record the deferral in the PROJECT pin and re-apply.
	if err := os.WriteFile(projPin, []byte(pin+"\nnative_agents = ['claude']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply", "--scope", "project", "--project", proj); err != nil {
		t.Fatalf("project apply after deferring: %v\n%s", err, out)
	}
	mustNotExist(t, "project-scope plugin subagent", projected)
}
