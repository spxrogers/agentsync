package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cleartext is the resolved value of ${secret:LEAK_TOK}. After apply it is
// substituted into the destination; it must NEVER survive a dest->source
// capture (import / reconcile write-back) into ~/.agentsync.
const cleartext = "ghp_LEAKVALUE_DO_NOT_PERSIST"

// setupCaptureHome initialises a fresh ~/.agentsync with the claude agent and
// the env secrets backend, and returns the agentsync home + the env map runCLI
// needs (AGENTSYNC_TARGET_ROOT redirection + the secret value in the env).
func setupCaptureHome(t *testing.T) (home string, env map[string]string) {
	t.Helper()
	tmp := t.TempDir()
	env = map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmp,
		"LEAK_TOK":              cleartext,
	}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}
	home = filepath.Join(tmp, ".agentsync")
	tomlPath := filepath.Join(home, "agentsync.toml")
	cfg, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tomlPath, append(cfg, []byte("\n[secrets]\nbackend = \"env\"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, env
}

func writeSource(t *testing.T, home, rel, content string) string {
	t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCapture_ImportNoSecretLeak is the table-driven leak guard: for every
// secret-bearing field of every capturable component, apply substitutes the
// secret to cleartext into the destination, import reads it back, and the
// canonical source must end up with the ${secret:…} placeholder restored and no
// cleartext written. One missing field in the walker would surface here as a
// leak in exactly that row.
// Project-overlay secret fields are covered at the unit level
// (secrets.TestReReferenceCanonical_ProjectOverlay + the walker-visitation
// test), not here: no dest->source capture path carries a Project overlay —
// import is user-scope (adapter.ScopeUser) and Capture writes top-level
// source files (mcp/<id>.toml, lsp/<id>.toml, hooks/<event>.toml), never a
// project overlay. So the overlay leak surface lives entirely in the walker /
// ReReferenceCanonical, which the secrets unit tests exercise directly.
func TestCapture_ImportNoSecretLeak(t *testing.T) {
	cases := []struct {
		name     string // sub-test name
		srcRel   string // source file (relative to ~/.agentsync)
		srcBody  string
		selector string // import selector
		// fields the walker must restore; each is a ${secret:…} substring the
		// re-referenced source file must contain.
		wantRefs []string
	}{
		{
			name:   "mcp_stdio_command_args_env",
			srcRel: "mcp/stdio.toml",
			srcBody: "" +
				"[server]\n" +
				"type = \"stdio\"\n" +
				"command = \"${secret:LEAK_TOK}\"\n" +
				"args = [\"${secret:LEAK_TOK}\", \"plain-arg\"]\n" +
				"[server.env]\n" +
				"GH_TOKEN = \"${secret:LEAK_TOK}\"\n",
			selector: "claude:mcp:stdio",
			wantRefs: []string{"${secret:LEAK_TOK}"},
		},
		{
			name:   "mcp_http_url_headers_env",
			srcRel: "mcp/http.toml",
			srcBody: "" +
				"[server]\n" +
				"type = \"http\"\n" +
				"url = \"${secret:LEAK_TOK}\"\n" +
				"[server.headers]\n" +
				"Authorization = \"Bearer ${secret:LEAK_TOK}\"\n" +
				"[server.env]\n" +
				"API_KEY = \"${secret:LEAK_TOK}\"\n",
			selector: "claude:mcp:http",
			wantRefs: []string{"${secret:LEAK_TOK}"},
		},

		{
			name:   "hook_command",
			srcRel: "hooks/PreToolUse.toml",
			srcBody: "" +
				"[[hook]]\n" +
				"type = \"command\"\n" +
				"command = \"audit ${secret:LEAK_TOK}\"\n",
			selector: "claude:hook:PreToolUse",
			wantRefs: []string{"${secret:LEAK_TOK}"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, env := setupCaptureHome(t)
			srcPath := writeSource(t, home, tc.srcRel, tc.srcBody)

			if _, err := runCLI(t, env, "apply"); err != nil {
				t.Fatalf("apply: %v", err)
			}
			// Precondition: apply really did substitute the cleartext into a dest.
			if !destContains(t, home, cleartext) {
				t.Fatalf("precondition failed: apply did not substitute the secret into any destination")
			}

			if _, err := runCLI(t, env, "import", tc.selector); err != nil {
				t.Fatalf("import %s: %v", tc.selector, err)
			}

			got, err := os.ReadFile(srcPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(got), cleartext) {
				t.Fatalf("import leaked cleartext into source %s:\n%s", tc.srcRel, got)
			}
			for _, ref := range tc.wantRefs {
				if !strings.Contains(string(got), ref) {
					t.Fatalf("import did not restore %q in source %s:\n%s", ref, tc.srcRel, got)
				}
			}
		})
	}
}

// destContains reports whether any rendered destination file under the target
// root holds substr. The target root is the parent of ~/.agentsync.
func destContains(t *testing.T, home, substr string) bool {
	t.Helper()
	root := filepath.Dir(home)
	found := false
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		// Skip the canonical source tree; we want DESTINATION files only.
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr == nil && strings.Contains(string(data), substr) {
			found = true
		}
		return nil
	})
	return found
}

// TestCapture_ReconcileNoSecretLeak exercises the OTHER capture path: reconcile
// [w]rite-back reconstructs an MCP spec from the cleartext destination and must
// re-reference every secret-bearing field before persisting source. The user
// edits an unrelated, non-secret field (command) to create the drift that makes
// write-back fire; the secret fields (args/env) must come back as placeholders.
func TestCapture_ReconcileNoSecretLeak(t *testing.T) {
	home, env := setupCaptureHome(t)
	srcPath := writeSource(t, home, "mcp/srv.toml", ""+
		"[server]\n"+
		"type = \"stdio\"\n"+
		"command = \"mybin\"\n"+ // non-secret, editable
		"args = [\"--token\", \"${secret:LEAK_TOK}\"]\n"+
		"[server.env]\n"+
		"GH_TOKEN = \"${secret:LEAK_TOK}\"\n")

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	destPath := filepath.Join(filepath.Dir(home), ".claude.json")
	dest, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dest), cleartext) {
		t.Fatalf("precondition: apply did not substitute the secret into %s:\n%s", destPath, dest)
	}
	edited := strings.Replace(string(dest), `"mybin"`, `"mybin2"`, 1)
	if edited == string(dest) {
		t.Fatalf("precondition: could not edit the non-secret command field:\n%s", dest)
	}
	if err := os.WriteFile(destPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "reconcile", "--auto-writeback"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), cleartext) {
		t.Fatalf("reconcile write-back leaked cleartext into source:\n%s", got)
	}
	if !strings.Contains(string(got), "${secret:LEAK_TOK}") {
		t.Fatalf("reconcile write-back did not restore the placeholder:\n%s", got)
	}
	if !strings.Contains(string(got), "mybin2") {
		t.Fatalf("reconcile write-back dropped the genuine non-secret edit (command):\n%s", got)
	}
}

// TestCapture_PreservesSourceOnlyFields proves capture preserves the
// source-only targeting fields (agents) that the rendered destination never
// carries for MCP — resetting them would silently broaden a server's exposure.
func TestCapture_PreservesSourceOnlyFields(t *testing.T) {
	for _, tc := range []struct {
		name     string
		srcRel   string
		srcBody  string
		selector string
	}{
		{
			name:   "mcp",
			srcRel: "mcp/srv.toml",
			// enabled = true keeps the server rendering (so import can find it)
			// while still exercising preservation of the explicit enabled flag.
			srcBody: "[server]\ntype = \"stdio\"\ncommand = \"npx\"\n" +
				"agents = [\"claude\"]\nenabled = true\n",
			selector: "claude:mcp:srv",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, env := setupCaptureHome(t)
			srcPath := writeSource(t, home, tc.srcRel, tc.srcBody)
			if _, err := runCLI(t, env, "apply"); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if _, err := runCLI(t, env, "import", tc.selector); err != nil {
				t.Fatalf("import: %v", err)
			}
			got, err := os.ReadFile(srcPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), "agents") || !strings.Contains(string(got), "claude") {
				t.Fatalf("capture dropped the source-only agents allowlist for %s (broadens exposure):\n%s", tc.name, got)
			}
			if !strings.Contains(string(got), "enabled") {
				t.Fatalf("capture dropped the source-only enabled flag for %s:\n%s", tc.name, got)
			}
		})
	}
}
