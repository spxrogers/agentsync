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

	// .claude.json should be gone (it was the only owned file).
	if _, err := os.Stat(claudePath); !os.IsNotExist(err) {
		t.Fatalf(".claude.json should be removed after --purge; stat err: %v", err)
	}

	// State should no longer have claude entries for Files/Keys.
	// (We verify indirectly: a second purge should report 0 paths.)
	out2, err2 := runCLI(t, env, "agent", "disable", "claude", "--purge")
	if err2 != nil {
		t.Fatalf("second agent disable --purge: %v\n%s", err2, out2)
	}
	if !strings.Contains(out2, "0 destination path(s)") {
		t.Fatalf("expected 0 paths on second purge; got: %s", out2)
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
