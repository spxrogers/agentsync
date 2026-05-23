package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReconcileWriteBack_ReReferencesSecrets is the regression for the
// reconcile [w]rite-back leak: apply substitutes ${secret:} into the dest as
// cleartext; when the user edits the dest and reconcile writes it back, the MCP
// spec was reconstructed from the cleartext dest and persisted to the source
// TOML verbatim — leaking the live token into a file users commit. The import
// path was hardened; reconcile write-back must re-reference too.
func TestReconcileWriteBack_ReReferencesSecrets(t *testing.T) {
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
	tomlPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	cfg, _ := os.ReadFile(tomlPath)
	_ = os.WriteFile(tomlPath, append(cfg, []byte("\n[secrets]\nbackend = \"env\"\n")...), 0o644)

	mcpPath := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcpPath), 0o755)
	src := "[server]\ntype = \"stdio\"\ncommand = \"npx\"\n[server.env]\nGH_TOKEN = \"${secret:GH_TOKEN}\"\n"
	_ = os.WriteFile(mcpPath, []byte(src), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// User hand-edits an UNRELATED field of the dest, creating drift.
	destPath := filepath.Join(tmp, ".claude.json")
	dest, _ := os.ReadFile(destPath)
	edited := strings.Replace(string(dest), `"npx"`, `"npm"`, 1)
	if edited == string(dest) {
		t.Fatalf("precondition: could not edit dest command:\n%s", dest)
	}
	_ = os.WriteFile(destPath, []byte(edited), 0o644)

	if _, err := runCLI(t, env, "reconcile", "--auto-writeback"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got, _ := os.ReadFile(mcpPath)
	if strings.Contains(string(got), "ghp_SUPERSECRET_DO_NOT_PERSIST") {
		t.Fatalf("reconcile write-back persisted the cleartext secret into source:\n%s", got)
	}
}
