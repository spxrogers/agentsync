package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegration_M2_OpenCodeMCPApply(t *testing.T) {
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

	mcpFile := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcpFile), 0o755)
	_ = os.WriteFile(mcpFile, []byte(`
[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
`), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	// Both Claude and OpenCode got the github MCP server.
	body, err := os.ReadFile(filepath.Join(tmp, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	if !strings.Contains(string(body), "github") {
		t.Fatalf("claude missing github: %s", body)
	}

	body, err = os.ReadFile(filepath.Join(tmp, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}
	if !strings.Contains(string(body), "github") {
		t.Fatalf("opencode missing github: %s", body)
	}
}
