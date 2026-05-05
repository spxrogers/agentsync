package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// makeLocalMarketplace creates a local directory tree that looks like a
// marketplace with one plugin. Returns the marketplace root path.
func makeLocalMarketplace(t *testing.T, dir string) string {
	t.Helper()
	mpDir := filepath.Join(dir, "fixture-marketplace")

	mpClaudePlugin := filepath.Join(mpDir, ".claude-plugin")
	if err := os.MkdirAll(mpClaudePlugin, 0o755); err != nil {
		t.Fatal(err)
	}
	mpJSON := `{
		"name": "test-mp",
		"owner": {"name": "tester"},
		"plugins": [
			{"name": "demo", "source": "./plugins/demo"},
			{"name": "inline-plugin", "source": "./plugins/demo", "strict": false,
			 "mcpServers": {"inline-srv": {"command": "echo", "args": ["inline"]}}}
		]
	}`
	if err := os.WriteFile(filepath.Join(mpClaudePlugin, "marketplace.json"), []byte(mpJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	pluginDir := filepath.Join(mpDir, "plugins", "demo", ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pluginJSON := `{
		"name": "demo",
		"version": "1.0.0",
		"mcpServers": {
			"demo-mcp": {"command": "${CLAUDE_PLUGIN_ROOT}/run.sh", "args": ["--port", "9090"]}
		}
	}`
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(pluginJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return mpDir
}

// makeGitMarketplace creates a local git repo that looks like a marketplace.
// Returns the file:// URL to use as the marketplace source.
func makeGitMarketplace(t *testing.T) string {
	t.Helper()
	workDir := t.TempDir()
	repo, err := gogit.PlainInit(workDir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}

	mpClaudePlugin := filepath.Join(workDir, ".claude-plugin")
	if err := os.MkdirAll(mpClaudePlugin, 0o755); err != nil {
		t.Fatal(err)
	}
	mpJSON := `{
		"name": "git-mp",
		"owner": {"name": "tester"},
		"plugins": [{"name": "git-plugin", "source": "./plugins/git-plugin"}]
	}`
	if err := os.WriteFile(filepath.Join(mpClaudePlugin, "marketplace.json"), []byte(mpJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(workDir, "plugins", "git-plugin", ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"name":"git-plugin","version":"0.1.0",
		"mcpServers":{"git-mcp":{"command":"git-run"}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(".")
	sig := &object.Signature{Name: "t", Email: "t@t", When: time.Now()}
	if _, err := w.Commit("init", &gogit.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return "file://" + workDir
}

// ---- marketplace add/remove/list tests -------------------------------------

func TestMarketplace_AddLocalPath(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	mpDir := makeLocalMarketplace(t, t.TempDir())
	out, err := runCLI(t, env, "marketplace", "add", mpDir)
	if err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}
	if !strings.Contains(out, "added marketplace") {
		t.Errorf("unexpected output: %s", out)
	}

	// Verify marketplace.toml was written.
	home := filepath.Join(tmp, ".agentsync")
	entries, err := os.ReadDir(filepath.Join(home, "marketplaces"))
	if err != nil {
		t.Fatalf("read marketplaces dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no marketplace toml written")
	}
}

func TestMarketplace_List(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	mpDir := makeLocalMarketplace(t, t.TempDir())
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "marketplace", "list")
	if err != nil {
		t.Fatalf("marketplace list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "test-mp") {
		t.Errorf("expected test-mp in list output: %s", out)
	}
}

func TestMarketplace_Remove(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	mpDir := makeLocalMarketplace(t, t.TempDir())
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "marketplace", "remove", "test-mp")
	if err != nil {
		t.Fatalf("marketplace remove: %v\n%s", err, out)
	}
	if !strings.Contains(out, "removed marketplace") {
		t.Errorf("expected removed message: %s", out)
	}

	// Verify it's gone from list.
	listOut, _ := runCLI(t, env, "marketplace", "list")
	if strings.Contains(listOut, "test-mp") {
		t.Errorf("marketplace still appears in list after remove: %s", listOut)
	}
}

func TestMarketplace_ListEmpty(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "marketplace", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no marketplaces") {
		t.Errorf("expected empty message: %s", out)
	}
}

// TestMarketplace_AddUpdatesState verifies that marketplace add writes the
// marketplace entry (with HeadSHA and FetchedAt) into .state/targets.json.
func TestMarketplace_AddUpdatesState(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	mpDir := makeLocalMarketplace(t, t.TempDir())
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}

	// Read state.json and verify marketplace entry.
	home := filepath.Join(tmp, ".agentsync")
	statePath := filepath.Join(home, ".state", "targets.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read targets.json: %v", err)
	}
	var st struct {
		Marketplaces map[string]struct {
			URL       string `json:"url"`
			FetchedAt string `json:"fetched_at"`
		} `json:"marketplaces"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("parse targets.json: %v", err)
	}
	if len(st.Marketplaces) == 0 {
		t.Fatalf("expected marketplace entry in state, got none; state=%s", data)
	}
	// The marketplace name should be "test-mp" (from the fixture's marketplace.json).
	entry, ok := st.Marketplaces["test-mp"]
	if !ok {
		t.Fatalf("marketplace test-mp not in state; keys=%v", st.Marketplaces)
	}
	if entry.URL == "" {
		t.Errorf("state marketplace URL is empty")
	}
	if entry.FetchedAt == "" {
		t.Errorf("state marketplace FetchedAt is empty")
	}
}

// TestMarketplace_RemoveUpdatesState verifies that marketplace remove clears
// the entry from .state/targets.json.
func TestMarketplace_RemoveUpdatesState(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	mpDir := makeLocalMarketplace(t, t.TempDir())
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "marketplace", "remove", "test-mp"); err != nil {
		t.Fatal(err)
	}

	// State should no longer have test-mp.
	home := filepath.Join(tmp, ".agentsync")
	statePath := filepath.Join(home, ".state", "targets.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read targets.json: %v", err)
	}
	var st struct {
		Marketplaces map[string]json.RawMessage `json:"marketplaces"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("parse targets.json: %v", err)
	}
	if _, found := st.Marketplaces["test-mp"]; found {
		t.Error("test-mp should have been removed from state after marketplace remove")
	}
}

// ---- plugin install/list/enable/disable/remove tests -----------------------

func TestPlugin_InstallFromLocalMarketplace(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	mpDir := makeLocalMarketplace(t, t.TempDir())
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "plugin", "install", "demo@test-mp")
	if err != nil {
		t.Fatalf("plugin install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "installed plugin demo") {
		t.Errorf("unexpected output: %s", out)
	}

	// Verify plugins/demo.toml exists.
	home := filepath.Join(tmp, ".agentsync")
	pluginPath := filepath.Join(home, "plugins", "demo.toml")
	if _, err := os.Stat(pluginPath); err != nil {
		t.Fatalf("demo.toml not written: %v", err)
	}
}

func TestPlugin_List(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	mpDir := makeLocalMarketplace(t, t.TempDir())
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "plugin", "install", "demo@test-mp"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "plugin", "list")
	if err != nil {
		t.Fatalf("plugin list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "demo") {
		t.Errorf("demo not in list: %s", out)
	}
}

func TestPlugin_ListEmpty(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "plugin", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no plugins") {
		t.Errorf("expected empty message: %s", out)
	}
}

func TestPlugin_EnableDisable(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	mpDir := makeLocalMarketplace(t, t.TempDir())
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "plugin", "install", "demo@test-mp"); err != nil {
		t.Fatal(err)
	}

	// Disable.
	out, err := runCLI(t, env, "plugin", "disable", "demo")
	if err != nil {
		t.Fatalf("plugin disable: %v\n%s", err, out)
	}
	if !strings.Contains(out, "disabled plugin demo") {
		t.Errorf("disable output: %s", out)
	}

	listOut, _ := runCLI(t, env, "plugin", "list")
	if !strings.Contains(listOut, "disabled") {
		t.Errorf("plugin should show disabled: %s", listOut)
	}

	// Enable.
	out, err = runCLI(t, env, "plugin", "enable", "demo")
	if err != nil {
		t.Fatalf("plugin enable: %v\n%s", err, out)
	}
	if !strings.Contains(out, "enabled plugin demo") {
		t.Errorf("enable output: %s", out)
	}

	listOut2, _ := runCLI(t, env, "plugin", "list")
	if strings.Contains(listOut2, "disabled") {
		t.Errorf("plugin should show enabled after re-enable: %s", listOut2)
	}
}

func TestPlugin_Remove(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	mpDir := makeLocalMarketplace(t, t.TempDir())
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "plugin", "install", "demo@test-mp"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "plugin", "remove", "demo")
	if err != nil {
		t.Fatalf("plugin remove: %v\n%s", err, out)
	}
	if !strings.Contains(out, "removed plugin demo") {
		t.Errorf("remove output: %s", out)
	}

	// Should be gone from list.
	listOut, _ := runCLI(t, env, "plugin", "list")
	if strings.Contains(listOut, "demo") {
		t.Errorf("plugin still in list after remove: %s", listOut)
	}
}

func TestPlugin_Upgrade(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	mpDir := makeLocalMarketplace(t, t.TempDir())
	if _, err := runCLI(t, env, "marketplace", "add", mpDir); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "plugin", "install", "demo@test-mp"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "plugin", "upgrade", "demo")
	if err != nil {
		t.Fatalf("plugin upgrade: %v\n%s", err, out)
	}
	if !strings.Contains(out, "upgraded plugin demo") {
		t.Errorf("upgrade output: %s", out)
	}
}

// TestPlugin_GitMarketplace exercises install via a git-backed marketplace.
func TestPlugin_GitMarketplace(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	repoURL := makeGitMarketplace(t)
	out, err := runCLI(t, env, "marketplace", "add", repoURL)
	if err != nil {
		t.Fatalf("marketplace add git: %v\n%s", err, out)
	}

	// After add, install the plugin from it.
	out, err = runCLI(t, env, "plugin", "install", "git-plugin@git-mp")
	if err != nil {
		t.Fatalf("plugin install from git marketplace: %v\n%s", err, out)
	}
	if !strings.Contains(out, "installed plugin git-plugin") {
		t.Errorf("install output: %s", out)
	}
}
