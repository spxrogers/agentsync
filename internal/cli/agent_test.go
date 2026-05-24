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

// TestAgent_AddPreservesSubtableForm is the regression for `agent add` bricking
// a config that uses the idiomatic [agents.<name>] sub-table form. writeAgents
// spliced by string-searching for the literal "[agents]" header; with
// sub-tables only that header is absent, so it appended a fresh [agents] block
// while leaving the old sub-tables — defining agents.<name> twice, which
// go-toml rejects on the next load, bricking the config via a normal command.
func TestAgent_AddPreservesSubtableForm(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	// Rewrite the config in the sub-table form (valid TOML the loader accepts).
	if err := os.WriteFile(cfgPath,
		[]byte("# my config\n[agents.claude]\nenabled = true\nscope = \"user\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "opencode"); err != nil {
		t.Fatalf("agent add on a sub-table-form config errored: %v", err)
	}
	// The config must still parse and list BOTH agents — not be bricked.
	out, err := runCLI(t, env, "agent", "list")
	if err != nil {
		t.Fatalf("config bricked after add (re-parse failed): %v\n%s", err, out)
	}
	if !strings.Contains(out, "claude") || !strings.Contains(out, "opencode") {
		t.Fatalf("expected both claude and opencode listed after add; got:\n%s", out)
	}
	// Preserved comment outside the agents section.
	cfg, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(cfg), "# my config") {
		t.Errorf("comment outside agents section was dropped:\n%s", cfg)
	}
}
