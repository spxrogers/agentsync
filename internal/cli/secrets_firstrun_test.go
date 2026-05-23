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

// writeSecretsConfig appends a [secrets] block to agentsync.toml using the
// supplied file/identity_file lines verbatim, so a test can reproduce the
// exact shape a user copies out of the init template.
func writeSecretsConfig(t *testing.T, tmp, recipient, fileLine, identityLine string) {
	t.Helper()
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	block := fmt.Sprintf("\n[secrets]\nbackend = \"age\"\nrecipient = %q\n%s%s\n",
		recipient, fileLine, identityLine)
	if err := os.WriteFile(cfgPath, append(body, []byte(block)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupFirstRunSecrets does init + agent add claude, generates an age identity
// under <home>/.config/agentsync/age.key, encrypts a github.token, and writes
// an MCP server that references it. It returns tmp and the recipient string.
// The [secrets] block is intentionally NOT written — each test writes its own
// shape so it can exercise the documented-template defaults.
func setupFirstRunSecrets(t *testing.T) (tmp string, env map[string]string, recipient string) {
	t.Helper()
	tmp = t.TempDir()
	env = map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

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

	// Encrypt to the DEFAULT age-file location (secrets/secrets.age) — the
	// same default `secrets set` / `secrets edit` write to.
	ageDest := filepath.Join(tmp, ".agentsync", "secrets", "secrets.age")
	if err := secrets_pkg.Encrypt([]byte("[github]\ntoken = \"ghp_firstrun\"\n"),
		id.Recipient().String(), ageDest); err != nil {
		t.Fatal(err)
	}

	mcpPath := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]

[server.env]
GITHUB_TOKEN = "${secret:github.token}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return tmp, env, id.Recipient().String()
}

// TestApply_SecretsDefaultFilePath is the regression for the bug where
// SelectBackend ignored the documented default [secrets].file location.
// `secrets set` and `doctor` default an empty `file` to secrets/secrets.age,
// but apply/verify/diff passed "" straight to age, so `os.Open("")` failed.
// A user who leaves `file` commented out (it is optional in the init
// template) could set + get secrets fine yet have apply break.
func TestApply_SecretsDefaultFilePath(t *testing.T) {
	tmp, env, recipient := setupFirstRunSecrets(t)
	// Omit `file` entirely; provide an absolute identity_file.
	idPath := filepath.Join(tmp, ".config", "agentsync", "age.key")
	writeSecretsConfig(t, tmp, recipient, "", fmt.Sprintf("identity_file = %q\n", idPath))

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply with default secrets.file failed: %v\n%s", err, out)
	}
	claudeJSON, err := os.ReadFile(filepath.Join(tmp, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	if !strings.Contains(string(claudeJSON), "ghp_firstrun") {
		t.Fatalf("resolved token not in .claude.json: %s", claudeJSON)
	}
}

// TestApply_SecretsEnvHomeIdentity is the regression for the bug where the
// apply path did not expand ${env:HOME} in identity_file. The init template
// literally ships identity_file = "${env:HOME}/.config/agentsync/age.key";
// doctor/verify expanded it for their stat check, but SelectBackend ->
// AgeBackend.load() called os.ReadFile on the literal "${env:HOME}/..." path,
// so a user who copied the documented config got a green doctor and a broken
// apply.
func TestApply_SecretsEnvHomeIdentity(t *testing.T) {
	tmp, env, recipient := setupFirstRunSecrets(t)
	writeSecretsConfig(t, tmp, recipient,
		"file = \"secrets/secrets.age\"\n",
		"identity_file = \"${env:HOME}/.config/agentsync/age.key\"\n")

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply with ${env:HOME} identity_file failed: %v\n%s", err, out)
	}
	claudeJSON, err := os.ReadFile(filepath.Join(tmp, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	if !strings.Contains(string(claudeJSON), "ghp_firstrun") {
		t.Fatalf("resolved token not in .claude.json: %s", claudeJSON)
	}
}

// TestApply_SecretsDocumentedTemplateShape combines both: the exact shape a
// user gets by uncommenting the init template's [secrets] block.
func TestApply_SecretsDocumentedTemplateShape(t *testing.T) {
	tmp, env, recipient := setupFirstRunSecrets(t)
	// As shipped in init.go's template (file present, ${env:HOME} identity).
	writeSecretsConfig(t, tmp, recipient,
		"file = \"secrets/secrets.age\"\n",
		"identity_file = \"${env:HOME}/.config/agentsync/age.key\"\n")

	if _, err := runCLI(t, env, "verify"); err != nil {
		t.Fatalf("verify with documented template shape failed: %v", err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply with documented template shape failed: %v\n%s", err, out)
	}
}
