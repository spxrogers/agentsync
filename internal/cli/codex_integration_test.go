package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestApply_Codex_MergesConfigTOML drives the full apply pipeline for the Codex
// adapter's TOML config.toml destination (the merge-toml-keys strategy). It is
// the end-to-end regression for: foreign-key survival, per-server foreign-collision
// backup, re-apply convergence (deterministic TOML output), and orphan cleanup
// when the last owned MCP server is removed.
func TestApply_Codex_MergesConfigTOML(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "codex"); err != nil {
		t.Fatal(err)
	}

	// Hand-edited ~/.codex/config.toml: a foreign top-level key (model), a
	// foreign sibling MCP server (other), and a conflicting github server that
	// agentsync is about to overwrite (so it must be backed up).
	cfgDir := tmp + "/.codex"
	_ = os.MkdirAll(cfgDir, 0o755)
	cfgPath := cfgDir + "/config.toml"
	original := `model = "gpt-5.5"
approval_policy = "on-request"

[mcp_servers.github]
command = "/usr/local/bin/my-fork"

[mcp_servers.other]
command = "keep-me"
`
	if err := os.WriteFile(cfgPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// Canonical mcp/github.toml that conflicts with the existing github key.
	mcpDir := tmp + "/.agentsync/mcp"
	_ = os.MkdirAll(mcpDir, 0o755)
	if err := os.WriteFile(mcpDir+"/github.toml",
		[]byte("[server]\ntype = \"stdio\"\ncommand = \"npx\"\nargs = [\"-y\",\"@m/server-github\"]\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "backed up") {
		t.Fatalf("apply did not advertise backup of the conflicting github server; got:\n%s", out)
	}

	got := parseTOMLFile(t, cfgPath)
	if got["model"] != "gpt-5.5" || got["approval_policy"] != "on-request" {
		t.Fatalf("foreign top-level keys lost: %#v", got)
	}
	servers, _ := got["mcp_servers"].(map[string]any)
	gh, _ := servers["github"].(map[string]any)
	if gh["command"] != "npx" {
		t.Fatalf("our github server not applied (command=%v): %#v", gh["command"], servers)
	}
	other, _ := servers["other"].(map[string]any)
	if other["command"] != "keep-me" {
		t.Fatalf("foreign sibling mcp_servers.other lost: %#v", servers)
	}

	// Second apply must converge: byte-identical config.toml (deterministic TOML
	// emission) and an "up to date" report rather than a fresh backup/rewrite.
	before, _ := os.ReadFile(cfgPath)
	out2, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("re-apply: %v\n%s", err, out2)
	}
	after, _ := os.ReadFile(cfgPath)
	if string(before) != string(after) {
		t.Fatalf("re-apply churned config.toml:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// Remove the only owned MCP server and re-apply: orphan cleanup must drop
	// mcp_servers.github while preserving the foreign keys and sibling server.
	if err := os.Remove(mcpDir + "/github.toml"); err != nil {
		t.Fatal(err)
	}
	if out3, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply after removal: %v\n%s", err, out3)
	}
	got = parseTOMLFile(t, cfgPath)
	servers, _ = got["mcp_servers"].(map[string]any)
	if _, stillThere := servers["github"]; stillThere {
		t.Fatalf("orphaned mcp_servers.github not removed: %#v", servers)
	}
	if got["model"] != "gpt-5.5" {
		t.Fatalf("orphan cleanup clobbered foreign key 'model': %#v", got)
	}
	if other, _ := servers["other"].(map[string]any); other["command"] != "keep-me" {
		t.Fatalf("orphan cleanup dropped foreign sibling mcp_servers.other: %#v", servers)
	}
}

// TestImport_Codex_SeedsPerPointerOwnership is the regression for the
// import-seed path missing the merge-toml-keys strategy: the codex MCP op was
// seeded as a whole-file state.Files entry instead of per-pointer state.Keys,
// so the imported server wasn't "owned" at pointer granularity and the next
// apply over a hand-edit spuriously reported a foreign-collision + backup.
func TestImport_Codex_SeedsPerPointerOwnership(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "codex"); err != nil {
		t.Fatal(err)
	}

	cfgDir := tmp + "/.codex"
	_ = os.MkdirAll(cfgDir, 0o755)
	cfgPath := cfgDir + "/config.toml"
	if err := os.WriteFile(cfgPath, []byte(`model = "gpt-5.5"

[mcp_servers.github]
command = "orig"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "import", "codex:mcp:github"); err != nil {
		t.Fatalf("import: %v", err)
	}

	// State must record per-pointer ownership of /mcp_servers/github, and NOT a
	// whole-file entry for config.toml.
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var st struct {
		Keys  map[string]json.RawMessage `json:"keys"`
		Files map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	var sawKey bool
	for k := range st.Keys {
		if strings.Contains(k, "/mcp_servers/github") {
			sawKey = true
		}
	}
	if !sawKey {
		t.Fatalf("import did not seed per-pointer ownership of /mcp_servers/github; keys=%v", keysOf(st.Keys))
	}
	for k := range st.Files {
		if strings.Contains(k, "config.toml") {
			t.Fatalf("import seeded a whole-file entry for config.toml (should be per-pointer): %s", k)
		}
	}

	// Behavioral proof: hand-edit the now-owned server and re-apply. Because the
	// server is owned at pointer granularity, apply overwrites it WITHOUT a
	// foreign-collision backup (the documented owned-key behavior) — with the
	// bug it would spuriously back up.
	if err := os.WriteFile(cfgPath, []byte(`model = "gpt-5.5"

[mcp_servers.github]
command = "hand-edited"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if strings.Contains(out, "backed up") {
		t.Fatalf("apply spuriously backed up an owned, imported server:\n%s", out)
	}
}

// TestImport_Codex_Plugin_UnresolvableMarketplace exercises `import codex:plugin`
// end-to-end: Codex records plugin enable-state but no marketplace fetch source,
// so a plugin whose marketplace isn't registered with agentsync is warned about
// and skipped (not an error), and nothing is written to the canonical source.
func TestImport_Codex_Plugin_UnresolvableMarketplace(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "codex"); err != nil {
		t.Fatal(err)
	}
	cfgDir := tmp + "/.codex"
	_ = os.MkdirAll(cfgDir, 0o755)
	if err := os.WriteFile(cfgDir+"/config.toml", []byte(`[plugins."gmail@team-mp"]
enabled = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "codex:plugin")
	if err != nil {
		t.Fatalf("import codex:plugin should not error on an unresolvable marketplace: %v\n%s", err, out)
	}
	// The plugin's marketplace ("team-mp") is registered nowhere, so it is named
	// in a skip warning rather than silently dropped.
	if !strings.Contains(out, "team-mp") {
		t.Fatalf("expected a warning naming the unresolvable marketplace; got:\n%s", out)
	}
	// Nothing should be written to the canonical source.
	if entries, _ := os.ReadDir(tmp + "/.agentsync/plugins"); len(entries) != 0 {
		t.Fatalf("expected no plugins/*.toml written; got %d entries", len(entries))
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestStatusDiff_Codex_CleanAfterApply is the regression for status/diff/reconcile
// not handling merge-toml-keys: they classified the JSON op.Content against the
// raw TOML config.toml (and read the dest as JSON), so a clean, just-applied
// config.toml showed permanent phantom drift and was never walked per-pointer.
func TestStatusDiff_Codex_CleanAfterApply(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "codex"); err != nil {
		t.Fatal(err)
	}
	mcpDir := tmp + "/.agentsync/mcp"
	_ = os.MkdirAll(mcpDir, 0o755)
	if err := os.WriteFile(mcpDir+"/github.toml",
		[]byte("[server]\ntype = \"stdio\"\ncommand = \"npx\"\nargs = [\"-y\",\"x\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	// status must walk config.toml PER POINTER (proving merge-toml-keys is
	// recognized) and report it clean — never conflict/drift.
	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "config.toml#/mcp_servers/github") {
		t.Fatalf("status did not classify config.toml per-pointer (merge-toml-keys unhandled):\n%s", out)
	}
	if strings.Contains(out, "conflict") || strings.Contains(out, "drift") {
		t.Fatalf("status reports phantom drift/conflict for a clean config.toml:\n%s", out)
	}

	// diff must show no hunk for the converged config.toml.
	out, err = runCLI(t, env, "diff")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	if strings.Contains(out, "--- source") && strings.Contains(out, "config.toml") {
		t.Fatalf("diff shows a phantom hunk for a clean config.toml:\n%s", out)
	}
}

// TestApply_Codex_HookOrphanCleanup is the regression for the orphan-cleanup
// strategy mismatch: codex hooks once lived in a JSON hooks.json (merge-json-keys)
// while the adapter's single KeyMergeStrategy() is merge-toml-keys, so removing
// the last hook synthesized a merge-toml-keys cleanup op against the JSON file
// and apply hard-failed on toml.Unmarshal. Hooks now live in config.toml, so
// cleanup uses the right format and succeeds.
func TestApply_Codex_HookOrphanCleanup(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "codex"); err != nil {
		t.Fatal(err)
	}
	hooksDir := tmp + "/.agentsync/hooks"
	_ = os.MkdirAll(hooksDir, 0o755)
	hookFile := hooksDir + "/PreToolUse.toml"
	if err := os.WriteFile(hookFile,
		[]byte("[[hook]]\nmatcher = \"Bash\"\ntype = \"command\"\ncommand = \"echo hi\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	cfgPath := tmp + "/.codex/config.toml"
	if got := parseTOMLFile(t, cfgPath); got["hooks"] == nil {
		t.Fatalf("hook not applied to config.toml: %#v", got)
	}

	// Remove the only hook and re-apply: orphan cleanup must succeed (not
	// hard-fail) and drop [hooks] from config.toml.
	if err := os.Remove(hookFile); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply after removing last hook hard-failed (orphan-cleanup strategy mismatch): %v\n%s", err, out)
	}
	// The PreToolUse event (and its command) must be gone. An empty `[hooks]`
	// table may remain — the same harmless artifact Claude leaves as `"hooks":{}`
	// in settings.json (the merge removes the owned leaf, not the empty parent).
	got := parseTOMLFile(t, cfgPath)
	if hooks, ok := got["hooks"].(map[string]any); ok && hooks["PreToolUse"] != nil {
		t.Fatalf("orphaned hook event not removed from config.toml: %#v", got)
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "echo hi") {
		t.Fatalf("orphaned hook command still present in config.toml:\n%s", raw)
	}
}

// TestReconcile_Writeback_CodexMCP verifies reconcile [w]rite-back captures a
// hand-edit to a Codex MCP server in config.toml. The dest is TOML and the
// pointer is /mcp_servers/<id> (Codex's native key), so write-back must read the
// dest as TOML and translate via codex.IngestMCPSpec — and capture.Capture must
// preserve the source-only agents/enabled fields.
func TestReconcile_Writeback_CodexMCP(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "codex"); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\nagents=[\"codex\"]\nenabled=true\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(tmp, ".codex", "config.toml")
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read codex dest: %v", err)
	}
	if !strings.Contains(string(body), `"npx"`) && !strings.Contains(string(body), `'npx'`) {
		t.Fatalf("codex dest missing expected mcp command:\n%s", body)
	}
	// Drift the dest command so write-back must rewrite the source.
	edited := strings.Replace(strings.Replace(string(body), `"npx"`, `"npm"`, 1), `'npx'`, `'npm'`, 1)
	_ = os.WriteFile(dst, []byte(edited), 0o644)

	if _, err := runCLI(t, env, "reconcile", "--auto-writeback"); err != nil {
		t.Fatalf("reconcile --auto-writeback (codex mcp): %v", err)
	}
	src, _ := os.ReadFile(mcp)
	if !strings.Contains(string(src), "npm") {
		t.Fatalf("codex mcp write-back didn't capture the dest edit:\n%s", src)
	}
	if !strings.Contains(string(src), "agents") || !strings.Contains(string(src), "enabled") {
		t.Fatalf("codex mcp write-back dropped source-only agents/enabled fields:\n%s", src)
	}
}

func parseTOMLFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := toml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
	return m
}
