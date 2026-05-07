package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExplain_PluginNotFound verifies that explain errors when the plugin id is unknown.
func TestExplain_PluginNotFound(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "explain", "nonexistent@mp")
	if err == nil {
		t.Fatal("expected error for unknown plugin; got nil")
	}
}

// TestExplain_TextOutput installs a plugin and verifies explain produces the
// per-agent translation table in text form.
func TestExplain_TextOutput(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	fixture := setupExplainFixture(t, tmp)

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

	out, err := runCLI(t, env, "explain", "demo@test-mp")
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, out)
	}
	if !strings.Contains(out, "demo@test-mp") {
		t.Fatalf("explain text output missing plugin label; got:\n%s", out)
	}
	if !strings.Contains(out, "claude") {
		t.Fatalf("explain text output missing claude agent; got:\n%s", out)
	}
}

// TestExplain_JSONOutput verifies --json emits parseable JSON with the expected fields.
func TestExplain_JSONOutput(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	fixture := setupExplainFixture(t, tmp)

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

	out, err := runCLI(t, env, "explain", "demo@test-mp", "--json")
	if err != nil {
		t.Fatalf("explain --json: %v\n%s", err, out)
	}

	var result struct {
		Rows []struct {
			Plugin   string `json:"plugin"`
			Agent    string `json:"agent"`
			Coverage string `json:"coverage"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("explain --json: not valid JSON: %v\noutput:\n%s", err, out)
	}
	if len(result.Rows) == 0 {
		t.Fatalf("explain --json returned zero rows; output:\n%s", out)
	}
	if result.Rows[0].Plugin == "" {
		t.Errorf("explain --json: first row has empty plugin field")
	}
}

// setupExplainFixture creates a minimal local marketplace with a single demo plugin.
func setupExplainFixture(t *testing.T, tmp string) string {
	t.Helper()
	fixture := filepath.Join(tmp, "fixture-marketplace-explain")
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
