package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegration_M4_PluginFanoutClaudeAndOpenCode exercises the full M4
// plugin pipeline end-to-end:
//   - set up a fake marketplace as a local relative-path fixture
//   - agentsync init + agent add claude + agent add opencode
//   - marketplace add <fixture>
//   - plugin install demo@test-mp
//   - apply
//   - verify both .claude.json and opencode.json contain demo-mcp
func TestIntegration_M4_PluginFanoutClaudeAndOpenCode(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	// Set up a fake marketplace as a local relative-path fixture.
	fixture := filepath.Join(tmp, "fixture-marketplace")
	if err := os.MkdirAll(filepath.Join(fixture, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, ".claude-plugin", "marketplace.json"),
		[]byte(`{
            "name": "test-mp",
            "owner": {"name": "x"},
            "plugins": [{"name": "demo", "source": "./plugins/demo"}]
        }`), 0o644); err != nil {
		t.Fatal(err)
	}

	plugDir := filepath.Join(fixture, "plugins", "demo", ".claude-plugin")
	if err := os.MkdirAll(plugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugDir, "plugin.json"),
		[]byte(`{
            "name": "demo",
            "version": "1.0.0",
            "mcpServers": {"demo-mcp": {"command":"echo","args":["hi"]}}
        }`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Bootstrap: init + agents.
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "opencode"); err != nil {
		t.Fatal(err)
	}

	// Marketplace add via local path.
	out, err := runCLI(t, env, "marketplace", "add", fixture)
	if err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}

	// Plugin install.
	out, err = runCLI(t, env, "plugin", "install", "demo@test-mp")
	if err != nil {
		t.Fatalf("plugin install: %v\n%s", err, out)
	}

	// Apply — this should project the plugin's MCP into both agent configs.
	out, err = runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	// Verify claude got demo-mcp.
	claudeBody, err := os.ReadFile(filepath.Join(tmp, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	if !strings.Contains(string(claudeBody), "demo-mcp") {
		t.Fatalf("claude missing demo-mcp; .claude.json:\n%s", claudeBody)
	}

	// Verify opencode got demo-mcp.
	opencodeBody, err := os.ReadFile(filepath.Join(tmp, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}
	if !strings.Contains(string(opencodeBody), "demo-mcp") {
		t.Fatalf("opencode missing demo-mcp; opencode.json:\n%s", opencodeBody)
	}

	// Verify the translation report shows both agents in the apply output.
	if !strings.Contains(out, "applied:") {
		t.Errorf("apply output missing 'applied:'; got: %s", out)
	}
	if !strings.Contains(out, "demo@test-mp") {
		t.Errorf("apply output missing translation report for demo@test-mp; got: %s", out)
	}
}

// TestIntegration_M4_SHAPinning verifies that manifest_sha is written at
// install time and that a re-upload (same version, different content) is
// detected as drift on the next update.
func TestIntegration_M4_SHAPinning(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	// Set up local marketplace.
	fixture := filepath.Join(tmp, "fixture-marketplace")
	if err := os.MkdirAll(filepath.Join(fixture, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"test-mp","owner":{"name":"x"},"plugins":[{"name":"demo","source":"./plugins/demo"}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	plugDir := filepath.Join(fixture, "plugins", "demo", ".claude-plugin")
	if err := os.MkdirAll(plugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pluginJSON := `{"name":"demo","version":"1.0.0","mcpServers":{"demo-mcp":{"command":"echo"}}}`
	if err := os.WriteFile(filepath.Join(plugDir, "plugin.json"), []byte(pluginJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "marketplace", "add", fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "plugin", "install", "demo@test-mp"); err != nil {
		t.Fatal(err)
	}

	// Verify plugins/demo.toml has a manifest_sha.
	home := filepath.Join(tmp, ".agentsync")
	pluginTOMLPath := filepath.Join(home, "plugins", "demo.toml")
	tomlData, err := os.ReadFile(pluginTOMLPath)
	if err != nil {
		t.Fatalf("read demo.toml: %v", err)
	}
	if !strings.Contains(string(tomlData), "manifest_sha") {
		t.Errorf("demo.toml missing manifest_sha field; got:\n%s", tomlData)
	}

	// Re-upload: the UPSTREAM marketplace serves different bytes at the SAME
	// version. update re-fetches the marketplace and compares the fresh upstream
	// manifest SHA against the recorded one. (Tampering the installed cache
	// instead would be a LOCAL tamper — caught by verifyPluginManifestSHA at
	// apply/load time, not by the read-only update poll.)
	reuploadedJSON := `{"name":"demo","version":"1.0.0","mcpServers":{"demo-mcp":{"command":"different-echo"}}}`
	if err := os.WriteFile(filepath.Join(plugDir, "plugin.json"), []byte(reuploadedJSON), 0o644); err != nil {
		t.Fatalf("re-upload plugin.json in the marketplace fixture: %v", err)
	}

	// update should detect SHA drift and emit a warning.
	out, err := runCLI(t, env, "update")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if !strings.Contains(out, "manifest-sha-mismatch") {
		t.Errorf("update should warn about SHA mismatch after re-upload; got: %s", out)
	}
}
