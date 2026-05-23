package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImport_DoesNotOverMaskNonSecretField is the regression for import
// over-masking: a non-secret field whose literal value happens to equal a
// secret's resolved value must NOT be rewritten to ${secret:…}. Here the
// command is literally "npx" and a secret (TOK) also resolves to "npx";
// import must keep command = "npx" and only re-reference the templated env.
func TestImport_DoesNotOverMaskNonSecretField(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmp,
		"TOK":                   "npx", // absurd, but proves field-positional scoping
	}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}
	tomlPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	cfg, _ := os.ReadFile(tomlPath)
	_ = os.WriteFile(tomlPath, append(cfg, []byte("\n[secrets]\nbackend = \"env\"\n")...), 0o644)

	mcpPath := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcpPath), 0o755)
	src := "[server]\ntype = \"stdio\"\ncommand = \"npx\"\n[server.env]\nTOK = \"${secret:TOK}\"\n"
	_ = os.WriteFile(mcpPath, []byte(src), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := runCLI(t, env, "import", "claude:mcp:github"); err != nil {
		t.Fatalf("import: %v", err)
	}

	got, _ := os.ReadFile(mcpPath)
	gs := string(got)
	if !strings.Contains(gs, "command = \"npx\"") && !strings.Contains(gs, "command = 'npx'") {
		t.Fatalf("non-secret command field was over-masked:\n%s", gs)
	}
	if !strings.Contains(gs, "${secret:TOK}") {
		t.Fatalf("templated env field should still be re-referenced:\n%s", gs)
	}
}
