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
