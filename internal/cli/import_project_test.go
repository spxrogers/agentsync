package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegration_Import_ProjectScope captures native PROJECT-scope config back
// into a project source tree and verifies it round-trips: a subsequent apply at
// project scope must not foreign-collide on the files it just imported (which
// only holds if state was seeded with the project scope + root keys).
func TestIntegration_Import_ProjectScope(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", proj); err != nil {
		t.Fatal(err)
	}

	// Native project-scope config: a subagent under <proj>/.claude/agents/ and an
	// MCP server in <proj>/.mcp.json (the upstream project-scope MCP location —
	// the file `claude mcp add --scope project` writes).
	claudeDir := filepath.Join(proj, ".claude")
	if err := os.MkdirAll(filepath.Join(claudeDir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "agents", "rev.md"),
		[]byte("---\ndescription: project reviewer\n---\nReview carefully.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".mcp.json"),
		[]byte(`{"mcpServers":{"projapi":{"command":"node","args":["s.js"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude", "--scope", "project", "--project", proj)
	if err != nil {
		t.Fatalf("import --scope project: %v\n%s", err, out)
	}

	// Captured config must land in the PROJECT tree, not the user home.
	if _, err := os.Stat(filepath.Join(proj, ".agentsync", "agents", "rev.md")); err != nil {
		t.Fatalf("project subagent not captured into project tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".agentsync", "mcp", "projapi.toml")); err != nil {
		t.Fatalf("project MCP not captured into project tree: %v", err)
	}
	// It must NOT have written into the user home.
	if _, err := os.Stat(filepath.Join(tmpHome, ".agentsync", "agents", "rev.md")); err == nil {
		t.Fatal("project import leaked into the user-scope home")
	}

	// Round-trip: a project-scope dry-run apply must not report a foreign
	// collision on the files we just imported (state was seeded for them).
	out, err = runCLI(t, env, "apply", "--project", proj, "--dry-run")
	if err != nil {
		t.Fatalf("apply --project --dry-run: %v\n%s", err, out)
	}
	if strings.Contains(out, "Foreign collision") {
		t.Fatalf("imported project files foreign-collided on next apply (state seed scope mismatch):\n%s", out)
	}
}

// TestIntegration_Import_ProjectPluginSkipped verifies a named project-scope
// plugin import is rejected (plugins are user-scope), and a bulk project import
// silently skips plugins.
func TestIntegration_Import_ProjectPluginSkipped(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", proj); err != nil {
		t.Fatal(err)
	}

	// Named plugin import at project scope is rejected (plugins are user-scope).
	if _, err := runCLI(t, env, "import", "claude:plugin:demo", "--scope", "project", "--project", proj); err == nil {
		t.Fatal("expected named project-scope plugin import to be rejected")
	}
	// Bulk plugin import at project scope silently skips (no error).
	if out, err := runCLI(t, env, "import", "claude:plugin", "--scope", "project", "--project", proj); err != nil {
		t.Fatalf("bulk project plugin import should skip cleanly, got: %v\n%s", err, out)
	}
}
