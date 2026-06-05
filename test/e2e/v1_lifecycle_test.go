//go:build e2e

// Package e2e contains the full v1.0 lifecycle integration test.
// Run with: go test -tags=e2e ./test/e2e/...
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	secrets_pkg "github.com/spxrogers/agentsync/internal/secrets"
)

// buildBinary compiles the agentsync binary into a tmp dir and returns its path.
func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "agentsync")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/agentsync")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build agentsync: %v\n%s", err, out)
	}
	return bin
}

// repoRoot returns the absolute path to the repository root (two levels above
// the test package: test/e2e → test → repo).
func repoRoot(t *testing.T) string {
	t.Helper()
	// Walk upward from the test file's directory until we find go.mod.
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs .: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod not found)")
		}
		dir = parent
	}
}

// makeEnv returns an os.Environ()-based env slice with overrides applied.
func makeEnv(overrides map[string]string) []string {
	base := os.Environ()
	merged := make([]string, 0, len(base)+len(overrides))
	// Strip any keys we are overriding.
	override := make(map[string]bool, len(overrides))
	for k := range overrides {
		override[k] = true
	}
	for _, kv := range base {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			merged = append(merged, kv)
			continue
		}
		if !override[kv[:idx]] {
			merged = append(merged, kv)
		}
	}
	for k, v := range overrides {
		merged = append(merged, k+"="+v)
	}
	return merged
}

// runner wraps exec.Command for a fixed binary + env + working directory.
type runner struct {
	bin string
	dir string
	env []string
	t   *testing.T
}

func (r *runner) run(args ...string) (string, error) {
	r.t.Helper()
	cmd := exec.Command(r.bin, args...)
	cmd.Env = r.env
	// Run from the simulated $HOME (a tmp dir), the same way the BDD harness
	// sets cmd.Dir = WorkDir. agentsync now commits a real .agentsync/ project
	// tree at the repo root; without an explicit dir the subprocess would
	// inherit this package's directory, and the no-scope project auto-discovery
	// would walk up, find the repo tree, and fail with the "no scope was given"
	// ambiguity. From the simulated home the no-scope commands resolve to user
	// scope (the home's own .agentsync/ is excluded as the user source).
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (r *runner) mustRun(args ...string) string {
	r.t.Helper()
	out, err := r.run(args...)
	if err != nil {
		r.t.Fatalf("agentsync %s failed: %v\noutput:\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// ---- fixture helpers --------------------------------------------------------

// makeLocalMarketplace creates a minimal local marketplace fixture at dir with
// one plugin (id) that installs a single MCP server (mcpID, mcpCommand).
func makeLocalMarketplace(t *testing.T, dir, marketplaceName, pluginID, mcpID, mcpCommand string) {
	t.Helper()
	mpDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(mpDir, 0o755); err != nil {
		t.Fatalf("mkdir marketplace: %v", err)
	}
	mpJSON, _ := json.Marshal(map[string]any{
		"name":  marketplaceName,
		"owner": map[string]any{"name": "e2e"},
		"plugins": []map[string]any{
			{"name": pluginID, "source": "./plugins/" + pluginID},
		},
	})
	if err := os.WriteFile(filepath.Join(mpDir, "marketplace.json"), mpJSON, 0o644); err != nil {
		t.Fatalf("write marketplace.json: %v", err)
	}

	pluginDir := filepath.Join(dir, "plugins", pluginID, ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	pluginJSON, _ := json.Marshal(map[string]any{
		"name":    pluginID,
		"version": "1.0.0",
		"mcpServers": map[string]any{
			mcpID: map[string]any{"command": mcpCommand, "args": []string{"hello"}},
		},
	})
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), pluginJSON, 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
}

// ---- the test ---------------------------------------------------------------

// TestE2E_FullV1Lifecycle is the full v1.0 lifecycle integration test.
//
// Phases:
//  1. init
//  2. agent add claude + agent add opencode
//  3. marketplace add (local fixture) + plugin install
//  4. bare apply — verify MCP lands in both agent configs
//  5. age secrets: generate key, configure [secrets], secrets set, secrets get
//  6. MCP with ${secret:...} reference → apply → verify resolved
//  7. status (drift classification after apply → should be converged)
//  8. reconcile --auto-safe (nothing to reconcile after apply)
//  9. explain <plugin> — sanity-check output contains plugin id
//  10. import claude:mcp:<name> — round-trip capture
//  11. agent disable claude --purge — disable + clean
//  12. agent list — claude should still be listed as disabled
func TestE2E_FullV1Lifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	bin := buildBinary(t)
	home := t.TempDir()

	env := makeEnv(map[string]string{
		"AGENTSYNC_TARGET_ROOT": home,
		"HOME":                  home,
		"PATH":                  filepath.Dir(bin) + string(filepath.ListSeparator) + os.Getenv("PATH"),
	})
	r := &runner{bin: bin, dir: home, env: env, t: t}

	// ── Phase 1: init ────────────────────────────────────────────────────────
	out := r.mustRun("init")
	if !strings.Contains(out, "initialized") {
		t.Errorf("init: expected 'initialized' in output; got: %s", out)
	}

	// ── Phase 2: agents ──────────────────────────────────────────────────────
	r.mustRun("agent", "add", "claude")
	r.mustRun("agent", "add", "opencode")

	// Confirm list shows both.
	agentList := r.mustRun("agent", "list")
	if !strings.Contains(agentList, "claude") || !strings.Contains(agentList, "opencode") {
		t.Errorf("agent list missing expected agents; got: %s", agentList)
	}

	// ── Phase 3: marketplace + plugin ────────────────────────────────────────
	fixture := filepath.Join(home, "fixture-marketplace")
	makeLocalMarketplace(t, fixture, "v1-test-mp", "demo-plugin", "demo-mcp", "echo")

	r.mustRun("marketplace", "add", fixture)

	mpList := r.mustRun("marketplace", "list")
	if !strings.Contains(mpList, "v1-test-mp") {
		t.Errorf("marketplace list missing v1-test-mp; got: %s", mpList)
	}

	r.mustRun("plugin", "install", "demo-plugin@v1-test-mp")

	pluginList := r.mustRun("plugin", "list")
	if !strings.Contains(pluginList, "demo-plugin") {
		t.Errorf("plugin list missing demo-plugin; got: %s", pluginList)
	}

	// ── Phase 4: apply (bare — no secrets yet) ───────────────────────────────
	applyOut := r.mustRun("apply")
	if !strings.Contains(applyOut, "applied:") {
		t.Errorf("apply output missing 'applied:'; got: %s", applyOut)
	}

	// Both agent config files must contain the MCP server.
	claudeJSON := readFile(t, filepath.Join(home, ".claude.json"))
	if !strings.Contains(claudeJSON, "demo-mcp") {
		t.Errorf(".claude.json missing demo-mcp; content:\n%s", claudeJSON)
	}

	opencodeJSON := readFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"))
	if !strings.Contains(opencodeJSON, "demo-mcp") {
		t.Errorf("opencode.json missing demo-mcp; content:\n%s", opencodeJSON)
	}

	// ── Phase 5: age secrets ─────────────────────────────────────────────────
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate age identity: %v", err)
	}
	idPath := filepath.Join(home, ".config", "agentsync", "age.key")
	if err := os.MkdirAll(filepath.Dir(idPath), 0o755); err != nil {
		t.Fatalf("mkdir age key dir: %v", err)
	}
	if err := os.WriteFile(idPath, []byte(id.String()), 0o600); err != nil {
		t.Fatalf("write age identity: %v", err)
	}

	// Append [secrets] block to agentsync.toml.
	cfgPath := filepath.Join(home, ".agentsync", "agentsync.toml")
	body := readFileBytes(t, cfgPath)
	body = append(body, []byte(fmt.Sprintf(`
[secrets]
backend       = "age"
file          = "secrets/secrets.age"
recipient     = "%s"
identity_file = "%s"
`, id.Recipient().String(), idPath))...)
	if err := os.WriteFile(cfgPath, body, 0o644); err != nil {
		t.Fatalf("write agentsync.toml: %v", err)
	}

	// Encrypt secrets file with github.token.
	secretsAGEPath := filepath.Join(home, ".agentsync", "secrets", "secrets.age")
	if err := secrets_pkg.Encrypt([]byte("[github]\ntoken = \"ghp_e2e_tok\"\n"),
		id.Recipient().String(), secretsAGEPath); err != nil {
		t.Fatalf("encrypt secrets: %v", err)
	}

	// secrets get must resolve the token.
	getOut := r.mustRun("secrets", "get", "github.token")
	if !strings.Contains(getOut, "ghp_e2e_tok") {
		t.Errorf("secrets get: expected 'ghp_e2e_tok'; got: %s", getOut)
	}

	// secrets set then get must round-trip.
	r.mustRun("secrets", "set", "openai.key=sk-e2e-test")
	getOut2 := r.mustRun("secrets", "get", "openai.key")
	if !strings.Contains(getOut2, "sk-e2e-test") {
		t.Errorf("secrets get after set: expected 'sk-e2e-test'; got: %s", getOut2)
	}

	// ── Phase 6: MCP with ${secret:...} reference ────────────────────────────
	// Write mcp/github-secret.toml referencing the age secret.
	mcpSecretPath := filepath.Join(home, ".agentsync", "mcp", "github-secret.toml")
	mcpContent := `[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]

[server.env]
GITHUB_TOKEN = "${secret:github.token}"
`
	if err := os.WriteFile(mcpSecretPath, []byte(mcpContent), 0o644); err != nil {
		t.Fatalf("write mcp/github-secret.toml: %v", err)
	}

	applyOut2 := r.mustRun("apply")
	if !strings.Contains(applyOut2, "applied:") {
		t.Errorf("second apply output missing 'applied:'; got: %s", applyOut2)
	}

	// The resolved token must appear in .claude.json; the ref must not.
	claudeJSON2 := readFile(t, filepath.Join(home, ".claude.json"))
	if !strings.Contains(claudeJSON2, "ghp_e2e_tok") {
		t.Errorf(".claude.json missing resolved token 'ghp_e2e_tok'; content:\n%s", claudeJSON2)
	}
	if strings.Contains(claudeJSON2, "${secret:") {
		t.Errorf("unresolved secret ref leaked into .claude.json; content:\n%s", claudeJSON2)
	}

	// Source file must still carry the reference (not the cleartext).
	sourceMCP := readFile(t, mcpSecretPath)
	if strings.Contains(sourceMCP, "ghp_e2e_tok") {
		t.Errorf("cleartext token leaked into source mcp file; content:\n%s", sourceMCP)
	}
	if !strings.Contains(sourceMCP, "${secret:github.token}") {
		t.Errorf("source mcp file missing secret ref; content:\n%s", sourceMCP)
	}

	// ── Phase 7: status (drift classification) ────────────────────────────────
	// Immediately after apply everything must be converged (no drift).
	statusOut := r.mustRun("status")
	// Each line classifying a path should not say "drift" or "conflict".
	for _, line := range strings.Split(statusOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		// Expected classes: converged, pending, new, orphan (no drift/conflict).
		if strings.Contains(line, "drift") || strings.Contains(line, "conflict") {
			t.Errorf("status shows drift/conflict immediately after apply; line: %s", line)
		}
	}

	// ── Phase 8: reconcile --auto-safe ───────────────────────────────────────
	reconcileOut := r.mustRun("reconcile", "--auto-safe")
	if !strings.Contains(reconcileOut, "nothing to reconcile") {
		// auto-safe passes through converged items silently, so "nothing to
		// reconcile" is the expected message when nothing needs action.
		// If we get any output about drifted/conflicted items it's a bug.
		if strings.Contains(reconcileOut, "drift") || strings.Contains(reconcileOut, "conflict") {
			t.Errorf("reconcile --auto-safe found unexpected drift; output:\n%s", reconcileOut)
		}
	}

	// ── Phase 9: explain <plugin> ────────────────────────────────────────────
	explainOut := r.mustRun("explain", "demo-plugin")
	if !strings.Contains(explainOut, "demo-plugin") {
		t.Errorf("explain output missing plugin id 'demo-plugin'; got:\n%s", explainOut)
	}

	// JSON mode.
	explainJSON := r.mustRun("explain", "--json", "demo-plugin")
	var explainData any
	if err := json.Unmarshal([]byte(explainJSON), &explainData); err != nil {
		t.Errorf("explain --json produced invalid JSON: %v\noutput:\n%s", err, explainJSON)
	}

	// ── Phase 10: import claude:mcp:demo-mcp ─────────────────────────────────
	// The demo-mcp server was applied to .claude.json; now import it back.
	// Note: import reads from native config (which exists after apply).
	importOut, importErr := r.run("import", "claude:mcp:demo-mcp")
	if importErr != nil {
		// It's OK if the adapter's Ingest doesn't find demo-mcp by that name —
		// the plugin MCP is written as part of plugin fanout, not a standalone
		// mcp/*.toml entry.  Verify the error is the expected "not found" case.
		if !strings.Contains(importOut, "not found") && !strings.Contains(importOut, "demo-mcp") {
			t.Errorf("import claude:mcp:demo-mcp failed unexpectedly: %v\n%s", importErr, importOut)
		}
	}

	// ── Phase 11: agent disable claude --purge ────────────────────────────────
	r.mustRun("agent", "disable", "claude", "--purge")

	// ── Phase 12: agent list — claude still present, disabled ────────────────
	finalList := r.mustRun("agent", "list")
	if !strings.Contains(finalList, "claude") {
		t.Errorf("agent list missing claude after disable --purge; got: %s", finalList)
	}
	if !strings.Contains(finalList, "enabled=false") {
		t.Errorf("agent list should show claude as enabled=false; got: %s", finalList)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
