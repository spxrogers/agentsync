package cli_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentList_MissingHome_FriendlyError covers P4: agent commands on an
// uninitialized home printed a raw Go open error instead of pointing the user
// at `agentsync init`.
func TestAgentList_MissingHome_FriendlyError(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_HOME": filepath.Join(tmp, "nope")}
	out, err := runCLI(t, env, "agent", "list")
	if err == nil {
		t.Fatalf("expected an error for a missing home; out=%s", out)
	}
	if !strings.Contains(out+err.Error(), "agentsync init") {
		t.Fatalf("error should point at `agentsync init`; got out=%q err=%v", out, err)
	}
}

// TestImport_TwoPartSelector_Rejected covers P5: a selector missing the item
// name (e.g. "claude:mcp") must fail with the expected grammar, not a
// misleading downstream "server \"\" not found".
func TestImport_TwoPartSelector_Rejected(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := runCLI(t, env, "import", "claude:mcp")
	if err == nil {
		t.Fatalf("expected an error for a 2-part selector; out=%s", out)
	}
	if !strings.Contains(out+err.Error(), "missing the item name") {
		t.Fatalf("expected a 'missing the item name' error; got out=%q err=%v", out, err)
	}
}
