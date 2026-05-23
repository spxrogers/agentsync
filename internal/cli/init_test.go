package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInit_ScaffoldsGitignoreForState is the launch-blocker regression: the
// scaffolded home must .gitignore /.state/, which holds local state and
// plaintext credential backups. Without it, the README-recommended
// `chezmoi add ~/.agentsync` / `git init` would commit secrets.
func TestInit_ScaffoldsGitignoreForState(t *testing.T) {
	tmp := t.TempDir()
	if _, err := runCLI(t, map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}, "init"); err != nil {
		t.Fatal(err)
	}
	gi, err := os.ReadFile(filepath.Join(tmp, ".agentsync", ".gitignore"))
	if err != nil {
		t.Fatalf("init did not scaffold .gitignore: %v", err)
	}
	if !strings.Contains(string(gi), "/.state/") {
		t.Fatalf(".gitignore does not exclude /.state/:\n%s", gi)
	}
}

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

// TestInit_RejectsBadCloneURL covers the scheme-validation path so a user
// who pastes "http://..." or a typo gets a clear error before go-git is
// even called.
func TestInit_RejectsBadCloneURL(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	out, err := runCLI(t, env, "init", "http://example.com/foo.git")
	if err == nil {
		t.Fatalf("init should reject http://; got:\n%s", out)
	}
	out, err = runCLI(t, env, "init", "rsync://example.com/foo")
	if err == nil {
		t.Fatalf("init should reject unsupported scheme; got:\n%s", out)
	}
}

// TestFirstRun_ActionableHints checks the polished first-run errors/empties:
// secrets before init points at init; status with no agents isn't silent.
func TestFirstRun_ActionableHints(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "secrets", "get", "x"); err == nil {
		t.Fatal("secrets get before init should error")
	} else if !strings.Contains(err.Error(), "init") {
		t.Fatalf("secrets-before-init error should point at init; got: %v", err)
	}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no agents enabled") {
		t.Fatalf("status with no agents should hint, not be silent; got: %q", out)
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
