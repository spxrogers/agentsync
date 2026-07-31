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
	if data, err := os.ReadFile(filepath.Join(tmp, ".claude.json")); err == nil {
		if strings.Contains(string(data), "toolkit-srv") {
			t.Errorf("the plugin's MCP server must not be projected to claude:\n%s", data)
		}
	}
	if data, err := os.ReadFile(filepath.Join(tmp, ".claude", "settings.json")); err == nil {
		if strings.Contains(string(data), "toolkit-guard") {
			t.Errorf("the plugin's hook must not be projected to claude (it would fire twice):\n%s", data)
		}
	}

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
