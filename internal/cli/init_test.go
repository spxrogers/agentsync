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
	required := []string{
		"mcp", "marketplaces", "plugins",
		"memory", "memory/fragments",
		"skills", "agents", "commands", "hooks", "lsp",
		"secrets", ".state",
	}
	for _, d := range required {
		if _, err := os.Stat(filepath.Join(home, d)); err != nil {
			t.Fatalf("missing dir %s: %v", d, err)
		}
	}
	// secrets/ must be 0700 so the age-encrypted file's parent does not
	// leak existence to other users on a shared box.
	info, err := os.Stat(filepath.Join(home, "secrets"))
	if err != nil {
		t.Fatalf("stat secrets: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("secrets dir mode %v leaks to non-owner", info.Mode().Perm())
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
