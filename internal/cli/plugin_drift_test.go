package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStatus_NudgesUndeclaredPlugins verifies status emits a note for a plugin
// enabled in Claude's native config that isn't declared in the canonical
// source, pointing the user at `import claude:plugin`.
func TestStatus_NudgesUndeclaredPlugins(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeLocalMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("test-mp", mpDir, "demo"))

	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "import claude:plugin") || !strings.Contains(out, "demo") {
		t.Fatalf("status should nudge about the undeclared plugin 'demo'; got:\n%s", out)
	}
}

// TestStatus_NoNudgeWhenPluginDeclared verifies that once a plugin is declared
// in the source, status stops nudging about it.
func TestStatus_NoNudgeWhenPluginDeclared(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeLocalMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("test-mp", mpDir, "demo"))

	// Declare demo in the canonical source (as `plugin install` would).
	pluginsDir := filepath.Join(tmp, ".agentsync", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "demo.toml"),
		[]byte("[plugin]\nid = \"demo@test-mp\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if strings.Contains(out, "import claude:plugin") {
		t.Fatalf("status must not nudge about a declared plugin; got:\n%s", out)
	}
}

// TestDoctor_ReportsUndeclaredPlugins verifies doctor's Plugins section lists a
// natively-installed plugin missing from source without failing the run.
func TestDoctor_ReportsUndeclaredPlugins(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeLocalMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("test-mp", mpDir, "demo"))

	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor should not fail on an informational plugin nudge: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Plugins") {
		t.Fatalf("doctor missing Plugins section; got:\n%s", out)
	}
	if !strings.Contains(out, "demo") || !strings.Contains(out, "import claude:plugin") {
		t.Fatalf("doctor should report undeclared plugin 'demo'; got:\n%s", out)
	}
}

// TestDoctor_PluginsOKWhenNoneInstalled verifies the Plugins section reports a
// clean state when no native plugins are installed.
func TestDoctor_PluginsOKWhenNoneInstalled(t *testing.T) {
	_, env := importTestEnv(t)
	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no undeclared native plugins") {
		t.Fatalf("doctor should report a clean plugin state; got:\n%s", out)
	}
}
