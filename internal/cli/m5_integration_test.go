package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scaffoldProjectMCP writes a project-scope MCP server into an already-created
// project tree (<projectDir>/.agentsync/mcp/<id>.toml).
func scaffoldProjectMCP(t *testing.T, projectDir, id, command string, args ...string) {
	t.Helper()
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = `"` + a + `"`
	}
	body := "[server]\ntype = \"stdio\"\ncommand = \"" + command + "\"\n"
	if len(quoted) > 0 {
		body += "args = [" + strings.Join(quoted, ", ") + "]\n"
	}
	dir := filepath.Join(projectDir, ".agentsync", "mcp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestIntegration_Project_Overlay exercises the project-local source tree
// end-to-end:
//   - init + agent add claude (user scope)
//   - init --scope project --project <dir> scaffolds <dir>/.agentsync/
//   - a project-scope MCP server in <dir>/.agentsync/mcp/
//   - chdir into project dir
//   - apply --scope project (walks up to the .agentsync/ tree from cwd)
//   - verify <project>/.mcp.json contains the project MCP
func TestIntegration_Project_Overlay(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", projectDir); err != nil {
		t.Fatal(err)
	}
	scaffoldProjectMCP(t, projectDir, "proj-mcp", "npx", "-y", "@proj/mcp")

	// chdir into project dir so auto-detect kicks in.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "apply", "--scope", "project")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	mcpPath := filepath.Join(projectDir, ".mcp.json")
	body, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read project .mcp.json: %v", err)
	}
	if !strings.Contains(string(body), "proj-mcp") {
		t.Fatalf("project-scope MCP not landed in .mcp.json: %s", body)
	}
}

// TestIntegration_Scope_AmbiguousNonInteractiveErrors verifies that running with
// no --scope inside a project tree, with no TTY (the test harness) and no way to
// prompt, fails closed rather than silently picking a scope.
func TestIntegration_Scope_AmbiguousNonInteractiveErrors(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", projectDir); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "apply") // no --scope, cwd is a project tree, non-TTY
	if err == nil {
		t.Fatalf("expected an ambiguous-scope error, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "no scope was given") {
		t.Fatalf("error should explain the ambiguity; got: %v", err)
	}
	// The --no-input flag forces the same fail-closed path explicitly.
	if _, err := runCLI(t, env, "apply", "--no-input"); err == nil {
		t.Fatal("expected --no-input to fail closed on ambiguous scope")
	}
}

// TestIntegration_ScopeProject_NoTreeErrors verifies --scope project with no
// project tree found (and --project at a treeless path) is a hard error, never a
// silent fall back to user scope.
func TestIntegration_ScopeProject_NoTreeErrors(t *testing.T) {
	tmpHome := t.TempDir()
	bare := t.TempDir() // no .agentsync/ tree here
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "apply", "--project", bare); err == nil {
		t.Fatal("expected --project at a treeless path to error")
	}

	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	if err := os.Chdir(bare); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "apply", "--scope", "project"); err == nil {
		t.Fatal("expected --scope project with no tree to error")
	}
}

// TestIntegration_Project_HomeNotMistakenForProject guards the auto-detect
// footgun: ~/.agentsync/ is itself a .agentsync/ directory, so running a command
// from the home that owns it must NOT flip to project scope (which would stop
// writing user-scope destinations). Here cwd == the target root, so walk-up finds
// <root>/.agentsync (the user home) and must reject it as a project.
func TestIntegration_Project_HomeNotMistakenForProject(t *testing.T) {
	tmpHome := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	// A user-scope MCP server so apply writes the user dest (~/.claude.json).
	mcpDir := filepath.Join(tmpHome, ".agentsync", "mcp")
	if err := os.MkdirAll(mcpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mcpDir, "u.toml"),
		[]byte("[server]\ntype = \"stdio\"\ncommand = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	if err := os.Chdir(tmpHome); err != nil { // cwd == the home that owns .agentsync/
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// User-scope dest must exist; no project dest under <tmpHome>/.claude/ may.
	if _, err := os.Stat(filepath.Join(tmpHome, ".claude.json")); err != nil {
		t.Fatalf("user-scope .claude.json not written — home was mistaken for a project: %v", err)
	}
}

// TestIntegration_Project_ExplicitFlag verifies --project <path> works without
// needing to chdir.
func TestIntegration_Project_ExplicitFlag(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", projectDir); err != nil {
		t.Fatal(err)
	}
	scaffoldProjectMCP(t, projectDir, "api-mcp", "node", "server.js")

	out, err := runCLI(t, env, "apply", "--project", projectDir)
	if err != nil {
		t.Fatalf("apply --project: %v\n%s", err, out)
	}

	mcpPath := filepath.Join(projectDir, ".mcp.json")
	body, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read project .mcp.json: %v", err)
	}
	if !strings.Contains(string(body), "api-mcp") {
		t.Fatalf("project MCP (api-mcp) not in .mcp.json: %s", body)
	}
}

// TestIntegration_Project_AgentsFilter verifies that a project tree whose
// agentsync.toml declares only claude restricts the project agent set, so
// opencode's project-scope files are not written.
func TestIntegration_Project_AgentsFilter(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "opencode"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", projectDir); err != nil {
		t.Fatal(err)
	}
	// Restrict the project agent set to claude only.
	cfg := "[agents]\nclaude = { enabled = true }\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".agentsync", "agentsync.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	scaffoldProjectMCP(t, projectDir, "only-claude", "node", "x.js")

	if _, err := runCLI(t, env, "apply", "--project", projectDir); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// opencode's project config should NOT exist.
	if _, err := os.Stat(filepath.Join(projectDir, ".opencode")); err == nil {
		t.Fatalf("opencode project config unexpectedly written")
	}
}

// TestIntegration_Project_Memory verifies that a project tree's memory/AGENTS.md
// renders to the agent's project-scope memory file.
func TestIntegration_Project_Memory(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", projectDir); err != nil {
		t.Fatal(err)
	}
	mem := "# Project Rules\nAlways write tests first."
	if err := os.WriteFile(filepath.Join(projectDir, ".agentsync", "memory", "AGENTS.md"), []byte(mem), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "apply", "--project", projectDir); err != nil {
		t.Fatalf("apply: %v", err)
	}

	claudeMD := filepath.Join(projectDir, "CLAUDE.md")
	body, err := os.ReadFile(claudeMD)
	if err != nil {
		t.Fatalf("read project CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(body), "Project Rules") {
		t.Fatalf("project memory content not in CLAUDE.md: %s", body)
	}
}

// TestIntegration_Project_OpenCode_MCP verifies project-scope apply writes
// OpenCode MCP servers to <root>/opencode.json (the repo root), NOT to
// <root>/.opencode/opencode.json — which OpenCode does not read. This is the
// CLI/apply-level counterpart to the adapter-unit project-scope test.
func TestIntegration_Project_OpenCode_MCP(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "opencode"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", projectDir); err != nil {
		t.Fatal(err)
	}
	scaffoldProjectMCP(t, projectDir, "oc-mcp", "node", "server.js")

	if out, err := runCLI(t, env, "apply", "--project", projectDir); err != nil {
		t.Fatalf("apply --project: %v\n%s", err, out)
	}

	rootCfg := filepath.Join(projectDir, "opencode.json")
	body, err := os.ReadFile(rootCfg)
	if err != nil {
		t.Fatalf("read project opencode.json at repo root: %v", err)
	}
	if !strings.Contains(string(body), "oc-mcp") {
		t.Fatalf("project MCP not in <root>/opencode.json: %s", body)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".opencode", "opencode.json")); err == nil {
		t.Fatal("project MCP wrongly written to .opencode/opencode.json (OpenCode does not read it)")
	}
}

// TestIntegration_Project_MCPJsonDriftDetected verifies status at project scope
// classifies a hand-edit to <root>/.mcp.json as drift — i.e. the project-MCP
// dest is wired into the status/diff/reconcile drift path (which share the same
// classifier) like any other dest, now that it lives at the repo root.
func TestIntegration_Project_MCPJsonDriftDetected(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", projectDir); err != nil {
		t.Fatal(err)
	}
	// Server id deliberately free of the substring "drift" so the drift
	// assertion below can only match the drift *class*, never the id in the path.
	scaffoldProjectMCP(t, projectDir, "apisrv", "npx", "-y", "@x/mcp")

	if out, err := runCLI(t, env, "apply", "--project", projectDir); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	mcpPath := filepath.Join(projectDir, ".mcp.json")
	body, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	// Hand-edit an agentsync-owned key to introduce drift.
	modified := strings.ReplaceAll(string(body), `"npx"`, `"npm"`)
	if modified == string(body) {
		t.Fatal("fixture did not contain the expected command to drift")
	}
	if err := os.WriteFile(mcpPath, []byte(modified), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "status", "--project", projectDir)
	if err != nil {
		t.Fatalf("status --project: %v\n%s", err, out)
	}
	if !strings.Contains(out, "drift") {
		t.Fatalf("status did not report drift on project .mcp.json:\n%s", out)
	}
}
