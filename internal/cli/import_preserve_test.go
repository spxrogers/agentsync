package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImport_PreservesSourceOnlyFields is the regression for import dropping
// source-only MCP fields (agents/enabled) that the rendered destination never
// carries. Reconstructing the server purely from the dest reset the agents
// allowlist to default-all — silently BROADENING the server's exposure — and
// cleared enabled. reconcile already preserves these; import must too.
func TestImport_PreservesSourceOnlyFields(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}
	mcpPath := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcpPath), 0o755)
	// A restrictive agents allowlist — a source-only field absent from the dest.
	src := "[server]\ntype = \"stdio\"\ncommand = \"npx\"\nagents = [\"claude\"]\n"
	_ = os.WriteFile(mcpPath, []byte(src), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := runCLI(t, env, "import", "claude:mcp:github"); err != nil {
		t.Fatalf("import: %v", err)
	}

	got, _ := os.ReadFile(mcpPath)
	if !strings.Contains(string(got), "claude") || !strings.Contains(string(got), "agents") {
		t.Fatalf("import dropped the source-only agents allowlist (broadens exposure):\n%s", got)
	}
}
