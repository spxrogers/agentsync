package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentDisable_Basic verifies that agent disable flips the enabled bit
// without removing any files.
func TestAgentDisable_Basic(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Disable (no --purge).
	out, err := runCLI(t, env, "agent", "disable", "claude")
	if err != nil {
		t.Fatalf("agent disable: %v\n%s", err, out)
	}
	if !strings.Contains(out, "disabled") {
		t.Fatalf("expected 'disabled' in output; got: %s", out)
	}

	// Check TOML has enabled=false.
	cfg, _ := os.ReadFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"))
	if !strings.Contains(string(cfg), "enabled = false") {
		t.Fatalf("expected enabled=false in config; got:\n%s", cfg)
	}
}

// TestAgentDisable_NotRegistered verifies that disabling an unregistered agent errors.
func TestAgentDisable_NotRegistered(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "agent", "disable", "claude")
	if err == nil {
		t.Fatal("expected error for unregistered agent; got nil")
	}
}

// TestAgentEnable_FlipsEnabled verifies the enable sub-command.
func TestAgentEnable_FlipsEnabled(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "disable", "claude"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "agent", "enable", "claude")
	if err != nil {
		t.Fatalf("agent enable: %v\n%s", err, out)
	}
	if !strings.Contains(out, "enabled") {
		t.Fatalf("expected 'enabled' in output; got: %s", out)
	}

	cfg, _ := os.ReadFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"))
	if !strings.Contains(string(cfg), "enabled = true") {
		t.Fatalf("expected enabled=true in config after enable; got:\n%s", cfg)
	}
}

// TestAgentDisable_Purge_RemovesDestFiles verifies that agent disable --purge
// removes destination files owned by agentsync for that agent.
func TestAgentDisable_Purge_RemovesDestFiles(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Write an MCP server and apply so state tracks the .claude.json file.
	mcpFile := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	if err := os.MkdirAll(filepath.Dir(mcpFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpFile, []byte(`[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	// Verify .claude.json exists after apply.
	claudePath := filepath.Join(tmp, ".claude.json")
	if _, err := os.Stat(claudePath); err != nil {
		t.Fatalf(".claude.json not created after apply: %v", err)
	}

	// Disable with --purge.
	out, err := runCLI(t, env, "agent", "disable", "claude", "--purge")
	if err != nil {
		t.Fatalf("agent disable --purge: %v\n%s", err, out)
	}
	if !strings.Contains(out, "purged") {
		t.Fatalf("expected 'purged' in output; got: %s", out)
	}

	// .claude.json is a key-merge dest: agentsync owns only /mcpServers/github,
	// not the whole file. Purge must PRUNE the owned pointer (so a user's
	// foreign keys would survive), not delete the file. With no other content,
	// the file remains as an empty object — but it must NOT be deleted, and the
	// owned server must be gone.
	after, statErr := os.ReadFile(claudePath)
	if statErr != nil {
		t.Fatalf(".claude.json must survive purge (key-merge dest, only pointers owned); read err: %v", statErr)
	}
	if strings.Contains(string(after), "github") {
		t.Fatalf("owned mcpServers/github should have been pruned from .claude.json; got: %s", after)
	}

	// State should no longer have claude entries for Files/Keys.
	// (We verify indirectly: a second purge should report nothing pruned.)
	out2, err2 := runCLI(t, env, "agent", "disable", "claude", "--purge")
	if err2 != nil {
		t.Fatalf("second agent disable --purge: %v\n%s", err2, out2)
	}
	if !strings.Contains(out2, "deleted 0 file(s), pruned owned keys from 0") {
		t.Fatalf("expected nothing to purge on second run; got: %s", out2)
	}
}

// TestAgentDisable_Purge_KeepsSharedFileOwnedByOtherAgent is the regression
// for the data-loss bug: claude and opencode both render skills to the SAME
// path ~/.claude/skills/<name>/SKILL.md, so apply records that path under
// BOTH agents' state. `agent disable opencode --purge` then deleted the
// shared SKILL.md (Delete skips backup) — destroying a file the still-enabled
// claude needs. Purge must skip paths another registered agent still owns.
func TestAgentDisable_Purge_KeepsSharedFileOwnedByOtherAgent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "opencode"); err != nil {
		t.Fatal(err)
	}

	// Author a skill — both adapters render it to ~/.claude/skills/demo/SKILL.md.
	skillDir := filepath.Join(tmp, ".agentsync", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	sharedSkill := filepath.Join(tmp, ".claude", "skills", "demo", "SKILL.md")
	if _, err := os.Stat(sharedSkill); err != nil {
		t.Fatalf("shared skill not created by apply: %v", err)
	}

	// Purge opencode — claude is still enabled and owns the same file.
	if _, err := runCLI(t, env, "agent", "disable", "opencode", "--purge"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(sharedSkill); err != nil {
		t.Fatalf("purging opencode destroyed a skill still owned by claude: %v", err)
	}
}

// TestAgentDisable_Purge_ProjectScopeOnlyTouchesProject verifies the scope
// filter on purge (#183): `agent disable claude --purge --project <root>`
// removes only claude's PROJECT-scope destinations for that root — the user's
// machine-wide destinations (also owned by claude) must survive untouched.
func TestAgentDisable_Purge_ProjectScopeOnlyTouchesProject(t *testing.T) {
	tmp := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	// A user-scope MCP server → ~/.claude.json keys owned by claude:user.
	userMCP := filepath.Join(tmp, ".agentsync", "mcp", "usersrv.toml")
	if err := os.MkdirAll(filepath.Dir(userMCP), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userMCP, []byte("[server]\ntype = \"stdio\"\ncommand = \"npx\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	// A project tree with its own claude declaration + MCP → <proj>/.mcp.json
	// keys owned by claude:project:<proj>.
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", proj); err != nil {
		t.Fatal(err)
	}
	declareProjectAgent(t, env, proj, "claude")
	scaffoldProjectMCP(t, proj, "projsrv", "node", "s.js")
	if _, err := runCLI(t, env, "apply", "--project", proj); err != nil {
		t.Fatal(err)
	}

	projMCPPath := filepath.Join(proj, ".mcp.json")
	if body, err := os.ReadFile(projMCPPath); err != nil || !strings.Contains(string(body), "projsrv") {
		t.Fatalf("fixture: project .mcp.json missing projsrv (err=%v):\n%s", err, body)
	}

	out, err := runCLI(t, env, "agent", "disable", "claude", "--purge", "--project", proj)
	if err != nil {
		t.Fatalf("agent disable --purge --project: %v\n%s", err, out)
	}

	// Project dest: claude's owned key pruned.
	if body, _ := os.ReadFile(projMCPPath); strings.Contains(string(body), "projsrv") {
		t.Fatalf("project purge did not prune the project-owned key:\n%s", body)
	}
	// User dest: untouched — the purge was scoped to the project.
	userBody, err := os.ReadFile(filepath.Join(tmp, ".claude.json"))
	if err != nil {
		t.Fatalf("user .claude.json must survive a project-scoped purge: %v", err)
	}
	if !strings.Contains(string(userBody), "usersrv") {
		t.Fatalf("project-scoped purge deleted user-scope keys:\n%s", userBody)
	}
	// And the project declaration was flipped to disabled, not the user's.
	projCfg, _ := os.ReadFile(filepath.Join(proj, ".agentsync", "agentsync.toml"))
	if !strings.Contains(string(projCfg), "claude = { enabled = false }") {
		t.Fatalf("project entry not disabled:\n%s", projCfg)
	}
	userCfg, _ := os.ReadFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"))
	if !strings.Contains(string(userCfg), `claude = { enabled = true, scope = "user" }`) {
		t.Fatalf("user entry must stay enabled:\n%s", userCfg)
	}
}

// TestAgentDisable_Purge_NoPurge verifies that without --purge, destination
// files are NOT removed.
func TestAgentDisable_Purge_NoPurge(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	mcpFile := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	if err := os.MkdirAll(filepath.Dir(mcpFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpFile, []byte(`[server]
type    = "stdio"
command = "npx"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	claudePath := filepath.Join(tmp, ".claude.json")
	if _, err := os.Stat(claudePath); err != nil {
		t.Fatalf(".claude.json not created after apply: %v", err)
	}

	// Disable without --purge.
	if _, err := runCLI(t, env, "agent", "disable", "claude"); err != nil {
		t.Fatal(err)
	}

	// .claude.json should still be there.
	if _, err := os.Stat(claudePath); err != nil {
		t.Fatalf(".claude.json should still exist after disable (no --purge): %v", err)
	}
}

// TestAgentDisable_Purge_UserScopeIsCrossScope pins the RATIFIED policy (issue
// #171): a plain USER-scope `agent disable <agent> --purge` is intentionally
// cross-scope — it prunes the agent's dest keys across every scope:project pair,
// including project-scope dests in checked-out repos. (The project-scoped form is
// isolated; see TestAgentDisable_Purge_ProjectScopeOnlyTouchesProject.)
func TestAgentDisable_Purge_UserScopeIsCrossScope(t *testing.T) {
	tmp := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	userMCP := filepath.Join(tmp, ".agentsync", "mcp", "usersrv.toml")
	if err := os.MkdirAll(filepath.Dir(userMCP), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userMCP, []byte("[server]\ntype = \"stdio\"\ncommand = \"npx\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", proj); err != nil {
		t.Fatal(err)
	}
	declareProjectAgent(t, env, proj, "claude")
	scaffoldProjectMCP(t, proj, "projsrv", "node", "s.js")
	if _, err := runCLI(t, env, "apply", "--project", proj); err != nil {
		t.Fatal(err)
	}

	// A plain USER-scope purge (no --scope/--project): ratified cross-scope.
	out, err := runCLI(t, env, "agent", "disable", "claude", "--purge")
	if err != nil {
		t.Fatalf("user-scope agent disable --purge: %v\n%s", err, out)
	}

	// User dest pruned.
	if body, _ := os.ReadFile(filepath.Join(tmp, ".claude.json")); strings.Contains(string(body), "usersrv") {
		t.Fatalf("user-scope purge did not prune the user-scope key:\n%s", body)
	}
	// Project dest ALSO pruned — the ratified cross-scope behavior.
	if body, _ := os.ReadFile(filepath.Join(proj, ".mcp.json")); strings.Contains(string(body), "projsrv") {
		t.Fatalf("ratified cross-scope user purge should also prune the project-scope key:\n%s", body)
	}
}
