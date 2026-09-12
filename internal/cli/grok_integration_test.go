package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGrokFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readGrokHooks(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	hooks, _ := top["hooks"].(map[string]any)
	return hooks
}

// Exercise real ownership state, foreign-collision backups, convergence,
// mixed-format orphan cleanup, and purge at both scopes.
func TestGrokApplyLifecycle(t *testing.T) {
	for _, scope := range []string{"user", "project"} {
		for _, cleanup := range []string{"remove-source", "purge"} {
			t.Run(scope+"/"+cleanup, func(t *testing.T) {
				root, project := t.TempDir(), t.TempDir()
				env := map[string]string{"AGENTSYNC_TARGET_ROOT": root, "GROK_HOME": filepath.Join(t.TempDir(), "must-not-use")}
				mustRun(t, env, "init")
				flags := []string{}
				sourceRoot, destDir := filepath.Join(root, ".agentsync"), filepath.Join(root, ".grok")
				if scope == "project" {
					flags = []string{"--scope", "project", "--project", project}
					mustRun(t, env, append([]string{"init"}, flags...)...)
					sourceRoot, destDir = filepath.Join(project, ".agentsync"), filepath.Join(project, ".grok")
				}
				mustRun(t, env, append([]string{"agent", "add", "grok"}, flags...)...)
				cfg := filepath.Join(destDir, "config.toml")
				hooks := filepath.Join(destDir, "hooks", "agentsync.json")
				writeGrokFixture(t, cfg, "[models]\ndefault='personal-model'\n[mcp_servers.owned]\ncommand='old'\n[mcp_servers.foreign]\ncommand='keep'\n")
				writeGrokFixture(t, hooks, `{"other":true,"hooks":{"Stop":[{"hooks":[{"type":"command","command":"foreign"}]}],"PreToolUse":[{"hooks":[{"type":"command","command":"old"}]}]}}`)
				mcpSource := filepath.Join(sourceRoot, "mcp", "owned.toml")
				hookSource := filepath.Join(sourceRoot, "hooks", "PreToolUse.toml")
				writeGrokFixture(t, mcpSource, "[server]\ntype='stdio'\ncommand='runner'\n[server.extra]\nstartup_timeout_sec=45\n")
				writeGrokFixture(t, hookSource, "[[hook]]\nmatcher='Bash'\ntype='command'\ncommand='check'\n")
				out, err := runCLI(t, env, append([]string{"apply"}, flags...)...)
				if err != nil {
					t.Fatalf("apply: %v\n%s", err, out)
				}
				if !strings.Contains(out, "backed up") {
					t.Fatal("foreign collision did not produce a backup")
				}
				got := parseTOMLFile(t, cfg)
				server := got["mcp_servers"].(map[string]any)["owned"].(map[string]any)
				if server["startup_timeout_sec"] != int64(45) {
					t.Fatalf("timeout changed type: %#v", server)
				}
				beforeConfig, err := os.ReadFile(cfg)
				if err != nil {
					t.Fatal(err)
				}
				beforeHooks, err := os.ReadFile(hooks)
				if err != nil {
					t.Fatal(err)
				}
				mustRun(t, env, append([]string{"apply"}, flags...)...)
				afterConfig, _ := os.ReadFile(cfg)
				afterHooks, _ := os.ReadFile(hooks)
				if !bytes.Equal(beforeConfig, afterConfig) || !bytes.Equal(beforeHooks, afterHooks) {
					t.Fatal("second apply churned native config")
				}
				mustRun(t, env, append([]string{"status", "--exit-code"}, flags...)...)
				if cleanup == "purge" {
					mustRun(t, env, append([]string{"agent", "disable", "grok", "--purge"}, flags...)...)
				} else {
					if err := os.Remove(hookSource); err != nil {
						t.Fatal(err)
					}
					mustRun(t, env, append([]string{"apply"}, flags...)...)
					if parseTOMLFile(t, cfg)["mcp_servers"].(map[string]any)["owned"] == nil {
						t.Fatal("hook cleanup removed MCP")
					}
					if err := os.Remove(mcpSource); err != nil {
						t.Fatal(err)
					}
					mustRun(t, env, append([]string{"apply"}, flags...)...)
				}
				got = parseTOMLFile(t, cfg)
				servers := got["mcp_servers"].(map[string]any)
				if servers["owned"] != nil || servers["foreign"] == nil || got["models"].(map[string]any)["default"] != "personal-model" {
					t.Fatalf("TOML cleanup clobbered foreign state: %#v", got)
				}
				h := readGrokHooks(t, hooks)
				if h["PreToolUse"] != nil || h["Stop"] == nil {
					t.Fatalf("JSON cleanup clobbered foreign events: %#v", h)
				}
				if _, err := os.Stat(filepath.Join(env["GROK_HOME"], "config.toml")); !os.IsNotExist(err) {
					t.Fatal("redirected target escaped to GROK_HOME")
				}
			})
		}
	}
}

func TestGrokImportAndReconcileMCP(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": root, "GROK_TEST_TOKEN": "synthetic-resolved-token"}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "grok")
	configPath := filepath.Join(root, ".agentsync", "agentsync.toml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	writeGrokFixture(t, configPath, string(config)+"\n[secrets]\nbackend='env'\n")
	cfg := filepath.Join(root, ".grok", "config.toml")
	writeGrokFixture(t, cfg, "[mcp_servers.remote]\nurl='https://example.test/old'\nheaders={Authorization='synthetic-resolved-token'}\nstartup_timeout_sec=45\n")
	mustRun(t, env, "import", "grok:mcp:remote")
	src := filepath.Join(root, ".agentsync", "mcp", "remote.toml")
	captured := parseTOMLFile(t, src)["server"].(map[string]any)
	if captured["headers"].(map[string]any)["Authorization"] != "synthetic-resolved-token" {
		t.Fatal("native header not imported")
	}
	// Establish a reference before apply so reconcile must preserve the
	// existing secret boundary and source-only targeting fields.
	writeGrokFixture(t, src, "[server]\ntype='http'\nurl='https://example.test/old'\nheaders={Authorization='${secret:GROK_TEST_TOKEN}'}\nagents=['grok']\nenabled=true\n[server.extra]\nstartup_timeout_sec=45\n")
	mustRun(t, env, "apply")
	writeGrokFixture(t, cfg, "[mcp_servers.remote]\nurl='https://example.test/new'\nheaders={Authorization='synthetic-resolved-token'}\nstartup_timeout_sec=60\n")
	mustRun(t, env, "reconcile", "--auto-writeback")
	captured = parseTOMLFile(t, src)["server"].(map[string]any)
	if captured["url"] != "https://example.test/new" || captured["headers"].(map[string]any)["Authorization"] != "${secret:GROK_TEST_TOKEN}" {
		t.Fatalf("Grok reconciliation lost fields/references: %#v", captured)
	}
	if captured["enabled"] != true || captured["agents"].([]any)[0] != "grok" || captured["extra"].(map[string]any)["startup_timeout_sec"] != int64(60) {
		t.Fatal("source-only/native extra fields lost")
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("synthetic-resolved-token")) {
		t.Fatal("resolved value leaked into canonical source")
	}
	mustRun(t, env, "apply")
	mustRun(t, env, "status", "--exit-code")
}

func TestGrokHookEnrichmentRetiresStaleSource(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": root}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "grok")
	path := filepath.Join(root, ".grok", "hooks", "agentsync.json")
	writeGrokFixture(t, path, `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"check"}]}]}}`)
	mustRun(t, env, "import", "grok:hook:PreToolUse")
	src := filepath.Join(root, ".agentsync", "hooks", "PreToolUse.toml")
	if _, err := os.Stat(src); err != nil {
		t.Fatal(err)
	}
	const enriched = `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"check","failClosed":true}]}]}}`
	writeGrokFixture(t, path, enriched)
	mustRun(t, env, "import", "grok")
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("stale hook source not retired")
	}
	mustRun(t, env, "apply")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != enriched {
		t.Fatal("apply overwrote a refused native hook after retirement")
	}
}
