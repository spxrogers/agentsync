package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerify_Empty(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")

	out, err := runCLI(t, env, "verify")
	if err != nil {
		t.Fatalf("verify on empty home: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("verify output missing 'ok': %s", out)
	}
}

func TestVerify_BadTOML(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")

	badPath := filepath.Join(tmp, ".agentsync", "mcp", "broken.toml")
	_ = os.MkdirAll(filepath.Dir(badPath), 0o755)
	_ = os.WriteFile(badPath, []byte("[server\nmissing-bracket"), 0o644)

	_, err := runCLI(t, env, "verify")
	if err == nil {
		t.Fatal("verify should fail on malformed TOML")
	}
}

func TestVerify_UnknownAgent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "agent", "add", "claude")

	cfg := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body, _ := os.ReadFile(cfg)
	body = append(body, []byte("\n[agents]\nbogus = { enabled = true }\n")...)
	_ = os.WriteFile(cfg, body, 0o644)

	_, err := runCLI(t, env, "verify")
	if err == nil {
		t.Fatal("verify should reject unknown agent name")
	}
}
