package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupPluginMarketplaceFixture creates a minimal local marketplace whose
// single "demo" plugin projects an MCP server named demo-mcp.
func setupPluginMarketplaceFixture(t *testing.T, tmp string) string {
	t.Helper()
	fixture := filepath.Join(tmp, "fixture-marketplace-preview")
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
	if err := os.WriteFile(filepath.Join(plugDir, "plugin.json"),
		[]byte(`{"name":"demo","version":"1.0.0","mcpServers":{"demo-mcp":{"command":"echo","args":["hi"]}}}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// TestDiff_IncludesPluginProjection is the regression for the preview-lies
// bug: `diff` (and `status`) loaded the canonical model with source.Load,
// which does NOT project installed plugins, while `apply` uses
// marketplace.LoadProjected, which does. So a user with an installed plugin saw a
// `diff` that omitted the plugin's MCP servers / skills / commands entirely,
// then `apply` wrote them anyway — the preview did not match the action.
func TestDiff_IncludesPluginProjection(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	fixture := setupPluginMarketplaceFixture(t, tmp)

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "marketplace", "add", fixture)
	mustRun(t, env, "plugin", "install", "demo@test-mp")

	// diff is run BEFORE apply: the plugin-projected demo-mcp server is in
	// the source plan but not yet on disk, so it must show as a pending change.
	out, err := runCLI(t, env, "diff")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	if !strings.Contains(out, "demo-mcp") {
		t.Fatalf("diff omitted plugin-projected demo-mcp (preview != apply); got:\n%s", out)
	}
}

// TestStatus_IncludesPluginProjection is the same regression for `status`.
func TestStatus_IncludesPluginProjection(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	fixture := setupPluginMarketplaceFixture(t, tmp)

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "marketplace", "add", fixture)
	mustRun(t, env, "plugin", "install", "demo@test-mp")

	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "demo-mcp") {
		t.Fatalf("status omitted plugin-projected demo-mcp; got:\n%s", out)
	}
}

func mustRun(t *testing.T, env map[string]string, args ...string) {
	t.Helper()
	if out, err := runCLI(t, env, args...); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}
