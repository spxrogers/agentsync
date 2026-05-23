package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImport_ReReferencesSecretsNotCleartext is the regression for a
// credential-persistence leak: `apply` substitutes ${secret:KEY} into the
// destination as cleartext, and `import` ingests that destination. Writing the
// ingested value verbatim back into ~/.agentsync/mcp/<id>.toml (a file users
// commit to dotfiles repos) persisted the live secret in cleartext. Import must
// re-reference known secret values back to their ${secret:KEY} placeholder.
func TestImport_ReReferencesSecretsNotCleartext(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmp,
		"GH_TOKEN":              "ghp_SUPERSECRET_DO_NOT_PERSIST",
	}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}

	// Enable the env secrets backend.
	tomlPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	cfg, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte("\n[secrets]\nbackend = \"env\"\n")...)
	if err := os.WriteFile(tomlPath, cfg, 0o644); err != nil {
		t.Fatal(err)
	}

	// Source MCP server that references the secret.
	mcpPath := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "[server]\ntype = \"stdio\"\ncommand = \"npx\"\n[server.env]\nGH_TOKEN = \"${secret:GH_TOKEN}\"\n"
	if err := os.WriteFile(mcpPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	dest, _ := os.ReadFile(filepath.Join(tmp, ".claude.json"))
	if !strings.Contains(string(dest), "ghp_SUPERSECRET_DO_NOT_PERSIST") {
		t.Fatalf("precondition failed: apply did not substitute the secret into the dest:\n%s", dest)
	}

	if _, err := runCLI(t, env, "import", "claude:mcp:github"); err != nil {
		t.Fatalf("import: %v", err)
	}

	got, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "ghp_SUPERSECRET_DO_NOT_PERSIST") {
		t.Fatalf("import persisted the cleartext secret into the source TOML:\n%s", got)
	}
	if !strings.Contains(string(got), "${secret:GH_TOKEN}") {
		t.Fatalf("import did not re-reference the ${secret:GH_TOKEN} placeholder:\n%s", got)
	}
}
