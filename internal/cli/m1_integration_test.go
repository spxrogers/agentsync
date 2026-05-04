package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegration_M1_ClaudeMCPApply(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Author an MCP file directly (the `mcp add` CLI lands in M4; for M1 we
	// exercise via direct file creation, which is the supported "vim-able"
	// path anyway).
	mcpFile := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcpFile), 0o755)
	_ = os.WriteFile(mcpFile, []byte(`
[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
agents  = ["claude"]
`), 0o644)

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	body, err := os.ReadFile(filepath.Join(tmp, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	var top map[string]any
	_ = json.Unmarshal(body, &top)
	s := top["mcpServers"].(map[string]any)["github"].(map[string]any)
	if !strings.HasPrefix(s["command"].(string), "npx") {
		t.Fatalf("github command = %v", s["command"])
	}
}
