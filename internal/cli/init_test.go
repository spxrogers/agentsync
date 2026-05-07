package cli_test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInit_FreshScaffold(t *testing.T) {
	tmp := t.TempDir()
	out, err := runCLI(t,
		map[string]string{"AGENTSYNC_TARGET_ROOT": tmp},
		"init")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	home := filepath.Join(tmp, ".agentsync")
	for _, d := range []string{"mcp", "marketplaces", "plugins", "memory", "memory/fragments", "skills", "secrets", ".state"} {
		if _, err := os.Stat(filepath.Join(home, d)); err != nil {
			t.Fatalf("missing dir %s: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "agentsync.toml")); err != nil {
		t.Fatalf("missing agentsync.toml: %v", err)
	}
}

func TestInit_RefusesPopulatedHome(t *testing.T) {
	tmp := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmp, ".agentsync"), 0o755)
	_ = os.WriteFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"), []byte("# already there"), 0o644)

	_, err := runCLI(t,
		map[string]string{"AGENTSYNC_TARGET_ROOT": tmp},
		"init")
	if err == nil {
		t.Fatalf("init should refuse to overwrite a populated home")
	}
}
