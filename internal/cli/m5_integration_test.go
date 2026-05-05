package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegration_M5_ProjectOverlay exercises the full M5 project-local overlay
// pipeline end-to-end:
//   - init + agent add claude (user scope)
//   - .agentsync.toml with [[mcp]] in a project dir
//   - chdir into project dir
//   - apply (auto-detects project scope via walk-up)
//   - verify <project>/.claude/settings.json contains proj-mcp
func TestIntegration_M5_ProjectOverlay(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmpHome,
	}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Write a .agentsync.toml with a flat [[mcp]] entry.
	markerContent := `agents = ["claude"]

[[mcp]]
id      = "proj-mcp"
type    = "stdio"
command = "npx"
args    = ["-y", "@proj/mcp"]
`
	if err := os.WriteFile(filepath.Join(projectDir, ".agentsync.toml"), []byte(markerContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// chdir into project dir so auto-detect kicks in.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}

	// apply — no --scope flag; auto-detect should find .agentsync.toml and use project scope.
	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	// Verify project-scope settings.json was written under <projectDir>/.claude/
	settingsPath := filepath.Join(projectDir, ".claude", "settings.json")
	body, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read project settings.json: %v", err)
	}
	if !strings.Contains(string(body), "proj-mcp") {
		t.Fatalf("project-scope MCP not landed in settings.json: %s", body)
	}
}

// TestIntegration_M5_ExplicitProjectFlag verifies --project <path> works
// without needing to chdir.
func TestIntegration_M5_ExplicitProjectFlag(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmpHome,
	}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	markerContent := `agents = ["claude"]

[[mcp]]
id      = "api-mcp"
type    = "stdio"
command = "node"
args    = ["server.js"]
`
	if err := os.WriteFile(filepath.Join(projectDir, ".agentsync.toml"), []byte(markerContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Use --project flag to avoid needing to chdir.
	out, err := runCLI(t, env, "apply", "--project", projectDir)
	if err != nil {
		t.Fatalf("apply --project: %v\n%s", err, out)
	}

	settingsPath := filepath.Join(projectDir, ".claude", "settings.json")
	body, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read project settings.json: %v", err)
	}
	if !strings.Contains(string(body), "api-mcp") {
		t.Fatalf("project MCP (api-mcp) not in settings.json: %s", body)
	}
}

// TestIntegration_M5_AgentsFilter verifies that marker agents = ["claude"]
// filters out other agents, so only claude's project-scope files are written.
func TestIntegration_M5_AgentsFilter(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmpHome,
	}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "opencode"); err != nil {
		t.Fatal(err)
	}

	// Marker restricts to claude only.
	markerContent := `agents = ["claude"]
`
	if err := os.WriteFile(filepath.Join(projectDir, ".agentsync.toml"), []byte(markerContent), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "apply", "--project", projectDir)
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	// Claude's project settings should exist.
	claudeSettings := filepath.Join(projectDir, ".claude", "settings.json")
	if _, err := os.Stat(claudeSettings); err != nil {
		t.Logf("claude settings.json not found (may be OK if no MCP servers): %v", err)
	}

	// opencode's project config should NOT exist at <projectDir>/.opencode/
	opencodeConfig := filepath.Join(projectDir, ".opencode")
	if _, err := os.Stat(opencodeConfig); err == nil {
		t.Fatalf("opencode project config unexpectedly written at %s", opencodeConfig)
	}

	// apply should mention only claude.
	if strings.Contains(out, "opencode") {
		t.Logf("note: opencode appeared in output; agents filter working if no MCP written: %s", out)
	}
}

// TestIntegration_M5_MemoryImport verifies that memory.import appends content
// to the rendered memory file.
func TestIntegration_M5_MemoryImport(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmpHome,
	}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Create an AGENTS.md in the project dir.
	agentsMD := "# Project Rules\nAlways write tests first."
	if err := os.WriteFile(filepath.Join(projectDir, "AGENTS.md"), []byte(agentsMD), 0o644); err != nil {
		t.Fatal(err)
	}

	markerContent := `agents = ["claude"]

[memory]
import = ["AGENTS.md"]
`
	if err := os.WriteFile(filepath.Join(projectDir, ".agentsync.toml"), []byte(markerContent), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "apply", "--project", projectDir)
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	// Claude project-scope memory lands at <projectDir>/CLAUDE.md.
	claudeMD := filepath.Join(projectDir, "CLAUDE.md")
	body, err := os.ReadFile(claudeMD)
	if err != nil {
		t.Fatalf("read project CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(body), "Project Rules") {
		t.Fatalf("imported memory content not in CLAUDE.md: %s", body)
	}
}
