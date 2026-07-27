package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Acceptance for #200 F4 (one verb set across the noun groups) and F5 (every
// peer component gets a read side).

func componentFixture(t *testing.T) (tmp string, env map[string]string) {
	t.Helper()
	tmp = t.TempDir()
	env = map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	skill := filepath.Join(tmp, ".agentsync", "skills", "tidy")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"),
		[]byte("---\nname: tidy\ndescription: tidy up\n---\nTidy.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(tmp, ".agentsync", "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "rev.md"),
		[]byte("---\ndescription: reviewer\n---\nReview.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return tmp, env
}

// TestComponentList_EveryPeerComponent is F5: mcp was the only component you
// could ask about.
func TestComponentList_EveryPeerComponent(t *testing.T) {
	_, env := componentFixture(t)

	for _, tc := range []struct{ noun, want string }{
		{"skill", "tidy"},
		{"subagent", "rev"},
	} {
		out, err := runCLI(t, env, tc.noun, "list")
		if err != nil {
			t.Fatalf("%s list: %v\n%s", tc.noun, err, out)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s list missing %q; got:\n%s", tc.noun, tc.want, out)
		}
		// F11's alias works here too.
		if out, err := runCLI(t, env, tc.noun, "ls"); err != nil || !strings.Contains(out, tc.want) {
			t.Errorf("%s ls: %v\n%s", tc.noun, err, out)
		}
	}
	// An empty component reports an honest empty state naming how to author one,
	// rather than printing nothing.
	out, err := runCLI(t, env, "hook", "list")
	if err != nil {
		t.Fatalf("hook list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hooks/<event>.toml") {
		t.Errorf("empty hook list should say how to author one; got:\n%s", out)
	}
	// There is deliberately no write side for these.
	if _, err := runCLI(t, env, "skill", "add", "x"); err == nil {
		t.Error("`skill add` should not exist — a skill is a directory, not flag-authorable")
	}
}

// TestMCPEnableDisable is F4's new pair: the enabled bit existed and the loader
// read it, but only a hand-edit could flip it.
func TestMCPEnableDisable(t *testing.T) {
	tmp, env := componentFixture(t)
	mustRun(t, env, "mcp", "add", "srv", "--command", "echo")
	tomlPath := filepath.Join(tmp, ".agentsync", "mcp", "srv.toml")

	if out, err := runCLI(t, env, "mcp", "disable", "srv"); err != nil {
		t.Fatalf("mcp disable: %v\n%s", err, out)
	}
	body, _ := readFileString(t, tomlPath)
	if !strings.Contains(body, "enabled = false") {
		t.Fatalf("disable did not set enabled=false:\n%s", body)
	}
	// Disabled keeps the DEFINITION — that is what separates it from remove.
	if out, err := runCLI(t, env, "mcp", "list"); err != nil || !strings.Contains(out, "srv") {
		t.Fatalf("disabled server should still be defined: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "mcp", "enable", "srv"); err != nil {
		t.Fatalf("mcp enable: %v\n%s", err, out)
	}
	body, _ = readFileString(t, tomlPath)
	if strings.Contains(body, "enabled = false") {
		t.Fatalf("enable did not clear the disabled bit:\n%s", body)
	}
	if _, err := runCLI(t, env, "mcp", "disable", "nosuch"); err == nil {
		t.Error("disabling an undefined server should error")
	}
}

// TestSecretListRemove is F4's other addition — and pins that `list` prints
// KEYS only, which is the whole reason it can exist without the exposure
// `secret edit` carries.
func TestSecretListRemove(t *testing.T) {
	env, _, _, _ := setupSecretsEnv(t)

	if _, err := runCLIWithStdin(t, env, "topsecret-value-abc\n", "secret", "set", "svc.token", "--stdin"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLIWithStdin(t, env, "another-value-def\n", "secret", "set", "other", "--stdin"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "secret", "list")
	if err != nil {
		t.Fatalf("secret list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "svc.token") || !strings.Contains(out, "other") {
		t.Fatalf("secret list missing keys; got:\n%s", out)
	}
	if strings.Contains(out, "topsecret-value-abc") || strings.Contains(out, "another-value-def") {
		t.Fatalf("secret list printed VALUES; it must print keys only:\n%s", out)
	}

	if out, err := runCLI(t, env, "secret", "remove", "other"); err != nil {
		t.Fatalf("secret remove: %v\n%s", err, out)
	}
	out, _ = runCLI(t, env, "secret", "list")
	if strings.Contains(out, "other") {
		t.Fatalf("removed key still listed:\n%s", out)
	}
	if !strings.Contains(out, "svc.token") {
		t.Fatalf("remove took out the wrong key:\n%s", out)
	}
	// A missing key is refused, and the vault is left alone.
	if _, err := runCLI(t, env, "secret", "remove", "nosuch"); err == nil {
		t.Error("removing an absent key should error")
	}
}

// TestRenamedGroupsHaveNoAliases pins the ratified alias policy: these are hard
// renames, so the old spellings must be gone rather than quietly working.
func TestRenamedGroupsHaveNoAliases(t *testing.T) {
	_, env := componentFixture(t)

	for _, args := range [][]string{
		{"plugin", "install", "x"},
		{"secrets", "list"},
		{"verify"},
	} {
		if _, err := runCLI(t, env, args...); err == nil {
			t.Errorf("%v should no longer resolve — these are hard renames", args)
		}
	}
}

// TestSubagentList_RefusesUnmigratedTree closes the hole the F5 listings opened
// in F1's fail-closed gate.
//
// `list` loads TOLERANTLY so a pending subagent migration does not stop you
// seeing your skills — but LoadTolerant does not read the legacy `agents/`
// directory, so the SUBAGENT listing would report "(no subagents)" to a user who
// has several. That is the exact silent-zero reading F1 exists to prevent, and
// it is worse in a listing than anywhere else: it is the command a user runs to
// CHECK whether their subagents survived the upgrade.
func TestSubagentList_RefusesUnmigratedTree(t *testing.T) {
	tmp, env := componentFixture(t)

	// Move the migrated tree back to the legacy layout, as an upgrading user's
	// tree looks before `agentsync migrate subagents`.
	home := filepath.Join(tmp, ".agentsync")
	if err := os.Rename(filepath.Join(home, "subagents"), filepath.Join(home, "agents")); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "subagent", "list")
	if err == nil {
		t.Fatalf("subagent list must REFUSE an unmigrated tree rather than report zero; got:\n%s", out)
	}
	// The refusal has to be actionable — naming the command that fixes it.
	if !strings.Contains(err.Error()+out, "migrate subagents") {
		t.Errorf("refusal does not name `agentsync migrate subagents`:\n%v\n%s", err, out)
	}
	// And it must not be mistakable for an empty tree.
	if strings.Contains(out, "no subagents") {
		t.Errorf("refusal still printed the empty-state hint:\n%s", out)
	}

	// The other listings stay TOLERANT on purpose: a pending subagent migration
	// is no reason to stop answering "what skills do I have?".
	if out, err := runCLI(t, env, "skill", "list"); err != nil || !strings.Contains(out, "tidy") {
		t.Errorf("skill list must still work with a pending subagent migration: %v\n%s", err, out)
	}

	// After migrating, the listing answers normally.
	if out, err := runCLI(t, env, "migrate", "subagents", "--no-input"); err != nil {
		t.Fatalf("migrate subagents: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "subagent", "list"); err != nil || !strings.Contains(out, "rev") {
		t.Errorf("subagent list after migration: %v\n%s", err, out)
	}
}
