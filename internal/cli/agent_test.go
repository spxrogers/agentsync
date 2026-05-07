package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgent_AddListRemove(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "opencode"); err != nil {
		t.Fatalf("agent add opencode: %v", err)
	}

	listOut, err := runCLI(t, env, "agent", "list")
	if err != nil {
		t.Fatalf("agent list: %v", err)
	}
	if !strings.Contains(listOut, "claude") || !strings.Contains(listOut, "opencode") {
		t.Fatalf("list missing entries: %s", listOut)
	}

	if _, err := runCLI(t, env, "agent", "remove", "opencode"); err != nil {
		t.Fatalf("agent remove: %v", err)
	}
	listOut2, _ := runCLI(t, env, "agent", "list")
	if strings.Contains(listOut2, "opencode") {
		t.Fatalf("list still contains removed agent: %s", listOut2)
	}

	cfg, _ := os.ReadFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"))
	if !strings.Contains(string(cfg), `claude = `) {
		t.Fatalf("config didn't preserve claude line:\n%s", cfg)
	}
}
