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
	tomlPath := filepath.Join(tmp, ".agentsync", "plugins", "toolkit.toml")
	adopted := strings.ReplaceAll(mustReadFile(t, tomlPath), "native_agents = ['claude']\n", "")
	if err := os.WriteFile(tomlPath, []byte(adopted), 0o644); err != nil {
		t.Fatal(err)
	}
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
	tomlPath := filepath.Join(tmp, ".agentsync", "plugins", "toolkit.toml")
	deferred := mustReadFile(t, tomlPath) + "\nnative_agents = ['claude']\n"
	if err := os.WriteFile(tomlPath, []byte(deferred), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply after deferring: %v\n%s", err, out)
	}
	for label, path := range claudeToolkitPaths(tmp) {
		mustNotExist(t, label, path)
	}
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
	tomlPath := filepath.Join(tmp, ".agentsync", "plugins", "toolkit.toml")
	deferred := mustReadFile(t, tomlPath) + "\nnative_agents = ['claude']\n"
	if err := os.WriteFile(tomlPath, []byte(deferred), 0o644); err != nil {
		t.Fatal(err)
	}
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
	tomlPath := filepath.Join(tmp, ".agentsync", "plugins", "toolkit.toml")
	deferred := mustReadFile(t, tomlPath) + "\nnative_agents = ['claude']\n"
	if err := os.WriteFile(tomlPath, []byte(deferred), 0o644); err != nil {
		t.Fatal(err)
	}

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
	adopted := strings.ReplaceAll(mustReadFile(t, tomlPath), "native_agents = ['claude']\n", "")
	if err := os.WriteFile(tomlPath, []byte(adopted), 0o644); err != nil {
		t.Fatal(err)
	}

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
	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
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
}
