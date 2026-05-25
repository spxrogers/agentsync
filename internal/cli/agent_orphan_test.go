package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgent_OrphanVisibilityAndReachablePurge covers the round-5 finding: after
// `agent remove`, the agent's rendered native config + state keys are orphaned.
// `status` must surface them (not silently accumulate), and `disable --purge`
// must be reachable for the now-deregistered agent to clean them up.
func TestAgent_OrphanVisibilityAndReachablePurge(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	// Deregister the agent — leaves ~/.claude.json content + state keys behind.
	if _, err := runCLI(t, env, "agent", "remove", "claude"); err != nil {
		t.Fatal(err)
	}

	// status must surface the orphan (even though no agent is now enabled).
	out, _ := runCLI(t, env, "status")
	if !strings.Contains(out, "claude") || !strings.Contains(out, "orphaned") {
		t.Fatalf("status must surface the orphaned removed agent; got:\n%s", out)
	}

	// disable --purge must be reachable for the removed agent and clean it up.
	if out, err := runCLI(t, env, "agent", "disable", "claude", "--purge"); err != nil {
		t.Fatalf("disable --purge of a removed agent must work: %v\n%s", err, out)
	}

	// No orphan should remain afterward.
	out2, _ := runCLI(t, env, "status")
	if strings.Contains(out2, "orphaned") {
		t.Fatalf("after purge, no orphan should remain; got:\n%s", out2)
	}
}
