package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	secrets_pkg "github.com/spxrogers/agentsync/internal/secrets"
)

// TestIntegration_M6_AgeSecretsResolveOnApply exercises the full secrets flow:
//   - init + agent add claude
//   - configure [secrets] block with age backend
//   - encrypt secrets.age containing github.token
//   - write mcp/github.toml with ${secret:github.token} in env
//   - apply
//   - verify .claude.json contains the resolved literal token
//   - verify the source mcp/github.toml still contains the ref (not leaked)
func TestIntegration_M6_AgeSecretsResolveOnApply(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Generate age identity.
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	idPath := filepath.Join(tmp, ".config", "agentsync", "age.key")
	if err := os.MkdirAll(filepath.Dir(idPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idPath, []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	// Append [secrets] block to agentsync.toml.
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte(fmt.Sprintf(`
[secrets]
backend       = "age"
file          = "secrets/secrets.age"
recipient     = "%s"
identity_file = "%s"
`, id.Recipient().String(), idPath))...)
	if err := os.WriteFile(cfgPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	// Encrypt secrets.age with github.token.
	plain := []byte(`[github]
token = "ghp_abc"
`)
	ageDestPath := filepath.Join(tmp, ".agentsync", "secrets", "secrets.age")
	if err := secrets_pkg.Encrypt(plain, id.Recipient().String(), ageDestPath); err != nil {
		t.Fatal(err)
	}

	// Write mcp/github.toml with ${secret:github.token} in env.
	mcpPath := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	mcpContent := `[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]

[server.env]
GITHUB_TOKEN = "${secret:github.token}"
`
	if err := os.WriteFile(mcpPath, []byte(mcpContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run apply.
	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply failed: %v\n%s", err, out)
	}

	// Verify .claude.json has the literal resolved token (not the reference).
	claudeJSON, err := os.ReadFile(filepath.Join(tmp, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	if !strings.Contains(string(claudeJSON), "ghp_abc") {
		t.Fatalf("resolved token not in .claude.json: %s", claudeJSON)
	}
	// The reference itself should be gone from the rendered output.
	if strings.Contains(string(claudeJSON), "${secret:") {
		t.Fatalf("unresolved secret ref leaked into .claude.json: %s", claudeJSON)
	}

	// Verify the source mcp/github.toml still contains the reference (not leaked).
	repoBytes, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(repoBytes), "ghp_abc") {
		t.Fatalf("cleartext token leaked into source repo file: %s", repoBytes)
	}
	if !strings.Contains(string(repoBytes), "${secret:github.token}") {
		t.Fatalf("source file should still contain the reference: %s", repoBytes)
	}
}

// TestIntegration_M6_MissingSecretBlocksApply verifies that apply errors when
// a ${secret:...} reference cannot be resolved (fail-loud).
func TestIntegration_M6_MissingSecretBlocksApply(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Generate age identity.
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	idPath := filepath.Join(tmp, ".config", "agentsync", "age.key")
	_ = os.MkdirAll(filepath.Dir(idPath), 0o755)
	_ = os.WriteFile(idPath, []byte(id.String()), 0o600)

	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body, _ := os.ReadFile(cfgPath)
	body = append(body, []byte(fmt.Sprintf(`
[secrets]
backend       = "age"
file          = "secrets/secrets.age"
recipient     = "%s"
identity_file = "%s"
`, id.Recipient().String(), idPath))...)
	_ = os.WriteFile(cfgPath, body, 0o644)

	// Encrypt secrets.age WITHOUT github.token.
	_ = secrets_pkg.Encrypt([]byte("[other]\nfoo = \"bar\"\n"), id.Recipient().String(),
		filepath.Join(tmp, ".agentsync", "secrets", "secrets.age"))

	// MCP file references a missing secret.
	mcpPath := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcpPath), 0o755)
	_ = os.WriteFile(mcpPath, []byte(`[server]
type    = "stdio"
command = "npx"

[server.env]
TOKEN = "${secret:github.token}"
`), 0o644)

	_, err = runCLI(t, env, "apply")
	if err == nil {
		t.Fatal("expected apply to fail when secret is missing")
	}
}
