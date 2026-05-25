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

// TestImport_TwoPartSelector_BulkComponent covers the bulk form: a selector
// without an item name (e.g. "claude:mcp") imports every entry of that
// component. With no native MCP servers present it reports a soft notice and
// exits cleanly rather than erroring.
func TestImport_TwoPartSelector_BulkComponent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := runCLI(t, env, "import", "claude:mcp")
	if err != nil {
		t.Fatalf("bulk component import should not error on an empty component; out=%s err=%v", out, err)
	}
	if !strings.Contains(out, "no mcp found") {
		t.Fatalf("expected a 'no mcp found' notice; got out=%q", out)
	}
}

// TestImport_TrailingColonSelector_Rejected keeps the typo guard: "claude:"
// names an empty component and must fail rather than silently importing the
// whole agent.
func TestImport_TrailingColonSelector_Rejected(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := runCLI(t, env, "import", "claude:")
	if err == nil {
		t.Fatalf("expected an error for a trailing-colon selector; out=%s", out)
	}
	if !strings.Contains(out+err.Error(), "component must be non-empty") {
		t.Fatalf("expected a 'component must be non-empty' error; got out=%q err=%v", out, err)
	}
}
