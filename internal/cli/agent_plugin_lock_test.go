package cli_test

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAgentAdd_AcquiresGlobalLock is the regression for the lost-update race:
// `agent add/remove/enable/disable` did a read-modify-write of agentsync.toml
// with no global lock, so a concurrent registration (or a racing apply reading
// the file) could lose an update.
func TestAgentAdd_AcquiresGlobalLock(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	assertCommandBlocksOnLock(t, env, tmp, "agent", "add", "claude")
}

// TestPluginDisable_AcquiresGlobalLock is the regression for plugin mutators
// (install/upgrade/enable/disable/remove) racing `update`'s locked rewrite of
// the same plugins/<id>.toml.
func TestPluginDisable_AcquiresGlobalLock(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	pdir := filepath.Join(tmp, ".agentsync", "plugins")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "demo.toml"),
		[]byte("[plugin]\nid = \"demo\"\nversion = \"1.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertCommandBlocksOnLock(t, env, tmp, "plugin", "disable", "demo")
}
