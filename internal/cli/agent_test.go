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

// TestAgent_AddRefusesUnsplicableTOML pins the writeAgents fail-closed
// backstop: the [agents] rewriter is line-based, so a multi-line string whose
// CONTENT contains an "[agents.…]" line would be mangled by the splice —
// previously bricking the config (the spliced file no longer parsed, so every
// later command failed to load the home). The rewrite must now be validated by
// re-parsing before the write, refusing and leaving the file untouched.
func TestAgent_AddRefusesUnsplicableTOML(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	// Valid TOML the line splicer cannot handle: a multi-line string carrying
	// an [agents.evil] line as CONTENT.
	body := "[agents]\nclaude = { enabled = true }\n\n[updates]\ndefault_mode = \"track\"\ndefault_interval = \"\"\"\n[agents.evil]\n\"\"\"\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "agent", "add", "opencode")
	if err == nil {
		t.Fatal("agent add on an unsplicable config must refuse, not corrupt the file")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("error should say the rewrite was refused; got: %v", err)
	}
	// The on-disk file must be byte-identical (still parseable) and later
	// commands must still work.
	after, _ := os.ReadFile(cfgPath)
	if string(after) != body {
		t.Fatalf("refused rewrite still modified the file:\n%s", after)
	}
	if out, lerr := runCLI(t, env, "agent", "list"); lerr != nil || !strings.Contains(out, "claude") {
		t.Fatalf("config unusable after refused add: %v\n%s", lerr, out)
	}
}

// TestAgent_ProjectScope_Lifecycle exercises the #183 project-scope agent
// commands end-to-end: add/list/disable/enable/remove against a project tree's
// agentsync.toml, leaving the user-scope config untouched throughout.
func TestAgent_ProjectScope_Lifecycle(t *testing.T) {
	tmp := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", proj); err != nil {
		t.Fatal(err)
	}
	userCfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	projCfgPath := filepath.Join(proj, ".agentsync", "agentsync.toml")
	userCfgBefore, _ := os.ReadFile(userCfgPath)

	// add writes the PROJECT toml — with no scope key (the file's location is
	// the scope) — and leaves the user config byte-identical.
	if _, err := runCLI(t, env, "agent", "add", "claude", "--scope", "project", "--project", proj); err != nil {
		t.Fatalf("agent add --scope project: %v", err)
	}
	projCfg, _ := os.ReadFile(projCfgPath)
	if !strings.Contains(string(projCfg), "claude = { enabled = true }") {
		t.Fatalf("project config missing scope-less claude entry:\n%s", projCfg)
	}
	if strings.Contains(string(projCfg), `scope = "user"`) {
		t.Fatalf("project config must not carry a user scope marker:\n%s", projCfg)
	}
	// The scaffold's guidance comments sit ABOVE the [agents] header precisely
	// so the section rewrite cannot swallow them — pin that.
	if !strings.Contains(string(projCfg), "# Author project components") {
		t.Fatalf("first project agent add dropped the scaffold guidance comments:\n%s", projCfg)
	}
	if after, _ := os.ReadFile(userCfgPath); string(after) != string(userCfgBefore) {
		t.Fatalf("project-scope agent add modified the USER config:\n%s", after)
	}

	// list reads the project declaration.
	out, err := runCLI(t, env, "agent", "list", "--project", proj)
	if err != nil {
		t.Fatalf("agent list --project: %v", err)
	}
	if !strings.Contains(out, "claude") || !strings.Contains(out, "enabled=true") {
		t.Fatalf("project list missing claude: %s", out)
	}

	// disable/enable flip the project entry.
	if _, err := runCLI(t, env, "agent", "disable", "claude", "--project", proj); err != nil {
		t.Fatalf("agent disable --project: %v", err)
	}
	projCfg, _ = os.ReadFile(projCfgPath)
	if !strings.Contains(string(projCfg), "claude = { enabled = false }") {
		t.Fatalf("disable did not flip the project entry:\n%s", projCfg)
	}
	if _, err := runCLI(t, env, "agent", "enable", "claude", "--project", proj); err != nil {
		t.Fatalf("agent enable --project: %v", err)
	}
	projCfg, _ = os.ReadFile(projCfgPath)
	if !strings.Contains(string(projCfg), "claude = { enabled = true }") {
		t.Fatalf("enable did not flip the project entry back:\n%s", projCfg)
	}

	// enable of an agent the project never declared points at the project add.
	if _, err := runCLI(t, env, "agent", "enable", "codex", "--project", proj); err == nil ||
		!strings.Contains(err.Error(), "--scope project") {
		t.Fatalf("expected project-scope registration hint; got: %v", err)
	}

	// remove deletes the project entry; user config still untouched.
	if _, err := runCLI(t, env, "agent", "remove", "claude", "--project", proj); err != nil {
		t.Fatalf("agent remove --project: %v", err)
	}
	projCfg, _ = os.ReadFile(projCfgPath)
	if strings.Contains(string(projCfg), "claude = {") {
		t.Fatalf("remove left the project entry behind:\n%s", projCfg)
	}
	if after, _ := os.ReadFile(userCfgPath); string(after) != string(userCfgBefore) {
		t.Fatalf("project-scope lifecycle modified the USER config:\n%s", after)
	}
}

// TestAgent_ProjectScope_RequiresTree pins the shared scope-resolution rule for
// agent commands: --project at a path with no .agentsync/ tree is a hard error
// pointing at project init, never a silent user-scope write.
func TestAgent_ProjectScope_RequiresTree(t *testing.T) {
	tmp := t.TempDir()
	bare := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "agent", "add", "claude", "--project", bare)
	if err == nil {
		t.Fatal("expected agent add --project at a treeless path to error")
	}
	if !strings.Contains(err.Error(), "init --scope project") {
		t.Fatalf("error should point at project init; got: %v", err)
	}
	// Nothing may have been registered at user scope as a fallback.
	cfg, _ := os.ReadFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"))
	if strings.Contains(string(cfg), "claude = {") {
		t.Fatalf("failed project add fell back to a user-scope write:\n%s", cfg)
	}
}
