package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeClaudeSettings writes a user-scope ~/.claude/settings.json under tmp.
func writeClaudeSettings(t *testing.T, tmp string, v any) {
	t.Helper()
	dir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// directoryMarketplaceSettings builds a settings.json value registering mpDir as
// a "directory" marketplace under mpID and enabling each plugin@mpID.
func directoryMarketplaceSettings(mpID, mpDir string, plugins ...string) map[string]any {
	enabled := map[string]any{}
	for _, p := range plugins {
		enabled[p+"@"+mpID] = true
	}
	return map[string]any{
		"extraKnownMarketplaces": map[string]any{
			mpID: map[string]any{"source": map[string]any{"source": "directory", "path": mpDir}},
		},
		"enabledPlugins": enabled,
	}
}

// TestImport_PluginsFromClaude is the core happy path: a Claude config with a
// registered (directory) marketplace and an enabled plugin is captured into the
// canonical source — both the marketplace and plugin TOMLs plus the agentsync
// caches — by re-fetching, mirroring `marketplace add` + `plugin install`.
func TestImport_PluginsFromClaude(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeLocalMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("test-mp", mpDir, "demo"))

	out, err := runCLI(t, env, "import", "claude:plugin")
	if err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}

	home := filepath.Join(tmp, ".agentsync")
	// Canonical records.
	if _, err := os.Stat(filepath.Join(home, "marketplaces", "test-mp.toml")); err != nil {
		t.Fatalf("marketplaces/test-mp.toml not written: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "demo.toml")); err != nil {
		t.Fatalf("plugins/demo.toml not written: %v\n%s", err, out)
	}
	// Caches seeded so projection works on the next apply.
	if _, err := os.Stat(filepath.Join(home, ".state", "cache", "marketplaces", "test-mp", ".claude-plugin", "marketplace.json")); err != nil {
		t.Fatalf("marketplace cache not seeded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".state", "cache", "plugins", "demo", ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("plugin cache not seeded: %v", err)
	}
	// The plugin TOML pins a marketplace-scoped id so projection can resolve
	// entry-level overrides.
	data, _ := os.ReadFile(filepath.Join(home, "plugins", "demo.toml"))
	if !strings.Contains(string(data), "demo@test-mp") {
		t.Fatalf("plugins/demo.toml missing marketplace-scoped id; got:\n%s", data)
	}
}

// TestImport_PluginsWarnsUnregisteredMarketplace verifies the warn-and-skip
// path: a plugin whose marketplace is registered in neither Claude's native
// config nor agentsync's own store (here 'claude-plugins-official', which Claude
// does not list in extraKnownMarketplaces and the user has not `marketplace
// add`ed) is reported and skipped without failing the import.
func TestImport_PluginsWarnsUnregisteredMarketplace(t *testing.T) {
	tmp, env := importTestEnv(t)
	writeClaudeSettings(t, tmp, map[string]any{
		"enabledPlugins": map[string]any{"github@claude-plugins-official": true},
	})

	out, err := runCLI(t, env, "import", "claude:plugin")
	if err != nil {
		t.Fatalf("import should warn+skip, not error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "claude-plugins-official") {
		t.Fatalf("expected a warning naming the unregistered marketplace; got:\n%s", out)
	}
	if !strings.Contains(out, "nor agentsync") {
		t.Fatalf("warning should reflect store-first resolution wording; got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "plugins", "github.toml")); !os.IsNotExist(err) {
		t.Fatalf("an unresolvable plugin must not be written; stat err=%v", err)
	}
}

// TestImport_PluginsFromAgentsyncStore is the fix's core behavior: a plugin
// enabled from a marketplace that is registered in agentsync's own store (via
// `marketplace add`) but NOT in Claude's extraKnownMarketplaces is resolved from
// the store and imported — no warning, no skip. This is exactly what the skip
// warning's "marketplace add then re-import" remediation promises.
func TestImport_PluginsFromAgentsyncStore(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeLocalMarketplace(t, t.TempDir()) // declared name "test-mp", plugin "demo"

	// Register the marketplace in agentsync's store (this is the `marketplace add`
	// the user runs). It is NOT added to Claude's native config below.
	if out, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}

	// Claude has the plugin enabled from test-mp but does not list test-mp in
	// extraKnownMarketplaces — mirroring a built-in/auto marketplace.
	writeClaudeSettings(t, tmp, map[string]any{
		"enabledPlugins": map[string]any{"demo@test-mp": true},
	})

	// Dry-run previews the plugin only; the marketplace is already registered, so
	// there is no marketplace line to "import".
	dry, err := runCLI(t, env, "import", "claude:plugin", "--dry-run")
	if err != nil {
		t.Fatalf("import --dry-run: %v\n%s", err, dry)
	}
	if !strings.Contains(dry, "plugins/demo.toml") {
		t.Fatalf("dry-run should preview the plugin; got:\n%s", dry)
	}
	if strings.Contains(dry, "marketplaces/test-mp.toml") {
		t.Fatalf("already-registered marketplace must not be previewed for import; got:\n%s", dry)
	}
	if strings.Contains(dry, "skipping") {
		t.Fatalf("registered marketplace must not be skipped; got:\n%s", dry)
	}

	// Real import writes the plugin TOML.
	out, err := runCLI(t, env, "import", "claude:plugin")
	if err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if strings.Contains(out, "skipping") {
		t.Fatalf("registered marketplace must not be skipped; got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "plugins", "demo.toml")); err != nil {
		t.Fatalf("plugins/demo.toml not written from store-resolved marketplace: %v\n%s", err, out)
	}
}

// TestImport_PluginsDryRun previews without fetching or writing any marketplace
// / plugin TOML or cache.
func TestImport_PluginsDryRun(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeLocalMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("test-mp", mpDir, "demo"))

	out, err := runCLI(t, env, "import", "claude:plugin", "--dry-run")
	if err != nil {
		t.Fatalf("import --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would import") {
		t.Fatalf("dry-run should preview; got:\n%s", out)
	}
	home := filepath.Join(tmp, ".agentsync")
	for _, rel := range []string{
		filepath.Join("marketplaces", "test-mp.toml"),
		filepath.Join("plugins", "demo.toml"),
		filepath.Join(".state", "cache", "plugins", "demo"),
	} {
		if _, err := os.Stat(filepath.Join(home, rel)); !os.IsNotExist(err) {
			t.Fatalf("dry-run must not create %s; stat err=%v", rel, err)
		}
	}
}

// TestImport_PluginNamedSelector imports a single named plugin, and errors on a
// name that no enabled plugin matches.
func TestImport_PluginNamedSelector(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeLocalMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("test-mp", mpDir, "demo"))

	if out, err := runCLI(t, env, "import", "claude:plugin:demo"); err != nil {
		t.Fatalf("import claude:plugin:demo: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "plugins", "demo.toml")); err != nil {
		t.Fatalf("named plugin import did not write plugins/demo.toml: %v", err)
	}

	if _, err := runCLI(t, env, "import", "claude:plugin:nope"); err == nil {
		t.Fatal("expected error importing a plugin that is not enabled")
	}
}

// TestImport_PluginsStoreBeatsNativeConfig pins the store-first precedence: when
// a marketplace is registered BOTH in agentsync's store and in the agent's
// native config, import resolves it from the store and does not re-fetch — in
// dry-run that means no "marketplaces/<mp>.toml" preview line (the native path
// would print one), while the plugin still imports.
func TestImport_PluginsStoreBeatsNativeConfig(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeLocalMarketplace(t, t.TempDir()) // declared name "test-mp", plugin "demo"

	// Register the marketplace in agentsync's store.
	if out, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}
	// AND register the same marketplace in Claude's native config.
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("test-mp", mpDir, "demo"))

	out, err := runCLI(t, env, "import", "claude:plugin", "--dry-run")
	if err != nil {
		t.Fatalf("import --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "plugins/demo.toml") {
		t.Fatalf("dry-run should preview the plugin; got:\n%s", out)
	}
	if strings.Contains(out, "marketplaces/test-mp.toml") {
		t.Fatalf("store-registered marketplace must win (no marketplace preview line); got:\n%s", out)
	}
}

// TestImport_RejectsHostilePluginNameWithoutLeak proves the import path's
// defense for a native plugin name a plugin author influences: a name that is
// separator-free (so it clears the path-traversal gate) but carries terminal
// control bytes is rejected by ValidateComponentID — not installed, and not
// echoed raw into the skip diagnostic. The warning prints the name sanitized
// (ESC+CR stripped, inert "[31m" residue kept), so no escape reaches the
// terminal and no plugins/<name>.toml is written.
func TestImport_RejectsHostilePluginNameWithoutLeak(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeLocalMarketplace(t, t.TempDir())
	evilName := "demo" + string(rune(0x1b)) + "[31m" + string(rune(0x0d))
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("test-mp", mpDir, evilName))

	out, err := runCLI(t, env, "import", "claude:plugin")
	if err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if strings.ContainsRune(out, rune(0x1b)) || strings.ContainsRune(out, rune(0x0d)) {
		t.Errorf("control byte from hostile plugin name leaked into import output: %q", out)
	}
	if !strings.Contains(out, "skipping plugin") || !strings.Contains(out, "demo[31m") {
		t.Errorf("import should skip the hostile plugin and carry its sanitized name; got:\n%s", out)
	}
	// The hostile name must never become a file in the canonical source.
	if entries, _ := os.ReadDir(filepath.Join(tmp, ".agentsync", "plugins")); len(entries) != 0 {
		t.Errorf("hostile plugin must not be written to plugins/; got %d entries", len(entries))
	}
}

// TestImport_FullAgentIncludesPlugins verifies plugins are part of a full
// `import claude`: the summary counts them and both file components and plugins
// land in the canonical source.
func TestImport_FullAgentIncludesPlugins(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeLocalMarketplace(t, t.TempDir())

	// A native MCP server (offline component) plus a registered plugin.
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"),
		[]byte(`{"mcpServers": {"github": {"type": "stdio", "command": "npx"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("test-mp", mpDir, "demo"))

	out, err := runCLI(t, env, "import", "claude")
	if err != nil {
		t.Fatalf("import claude: %v\n%s", err, out)
	}
	if !strings.Contains(out, "plugin") {
		t.Fatalf("full-agent summary should mention plugins; got:\n%s", out)
	}
	home := filepath.Join(tmp, ".agentsync")
	for _, rel := range []string{
		filepath.Join("mcp", "github.toml"),
		filepath.Join("plugins", "demo.toml"),
		filepath.Join("marketplaces", "test-mp.toml"),
	} {
		if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
			t.Fatalf("full-agent import did not write %s: %v", rel, err)
		}
	}
}
