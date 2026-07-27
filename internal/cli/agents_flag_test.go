package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The acceptance suite for #200 F10 — one `--agents` grammar across the daily
// loop — and F11's aliases.

func agentsFlagFixture(t *testing.T) (tmp string, env map[string]string) {
	t.Helper()
	tmp = t.TempDir()
	env = map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "opencode")
	subDir := filepath.Join(tmp, ".agentsync", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "rev.md"),
		[]byte("---\ndescription: reviewer\n---\nReview.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return tmp, env
}

// TestAgentsFlag_ApplyNarrows is the headline gap F10 names: apply had no
// --agents at all, so the filter used by status/diff/reconcile vanished at the
// one step that writes.
func TestAgentsFlag_ApplyNarrows(t *testing.T) {
	tmp, env := agentsFlagFixture(t)

	if out, err := runCLI(t, env, "apply", "--agents", "claude"); err != nil {
		t.Fatalf("apply --agents claude: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".claude", "agents", "rev.md")); err != nil {
		t.Fatalf("claude should have been applied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".config", "opencode", "agents", "rev.md")); !os.IsNotExist(err) {
		t.Fatalf("opencode was excluded by --agents but rendered anyway (err=%v)", err)
	}
}

// TestAgentsFlag_IdenticalParsingEverywhere pins F10's actual requirement: not
// just "the flag exists" but that all five reject the same way. A per-command
// copy of the parsing is exactly how these drift.
func TestAgentsFlag_IdenticalParsingEverywhere(t *testing.T) {
	_, env := agentsFlagFixture(t)

	for _, cmd := range []string{"apply", "status", "diff", "reconcile"} {
		t.Run(cmd+"/empty", func(t *testing.T) {
			_, err := runCLI(t, env, cmd, "--agents", "")
			if err == nil {
				t.Fatal("empty --agents must be rejected, not read as \"none\"")
			}
			if !strings.Contains(err.Error(), "--agents cannot be empty") {
				t.Errorf("unexpected message: %v", err)
			}
		})
		t.Run(cmd+"/unknown", func(t *testing.T) {
			_, err := runCLI(t, env, cmd, "--agents", "nosuchagent")
			if err == nil {
				t.Fatal("an unknown agent must be rejected")
			}
			if !strings.Contains(err.Error(), "unknown agent") {
				t.Errorf("unexpected message: %v", err)
			}
		})
		t.Run(cmd+"/star", func(t *testing.T) {
			if out, err := runCLI(t, env, cmd, "--agents", "*"); err != nil {
				t.Fatalf(`--agents "*" should mean all enabled: %v\n%s`, err, out)
			}
		})
	}
}

// TestAgentsFlag_RevertStaysMutuallyExclusive keeps F10 from turning revert's
// selector into a fourth grammar: --agents is a spelling of the positional
// form, so combining them is refused rather than silently resolved.
func TestAgentsFlag_RevertStaysMutuallyExclusive(t *testing.T) {
	_, env := agentsFlagFixture(t)

	if _, err := runCLI(t, env, "revert", "--agents", "claude", "--all"); err == nil {
		t.Fatal("--agents with --all must be refused")
	}
	if _, err := runCLI(t, env, "revert", "--agents", "claude", "claude"); err == nil {
		t.Fatal("--agents with a positional agent must be refused")
	}
	if _, err := runCLI(t, env, "revert", "--agents", ""); err == nil {
		t.Fatal("empty --agents must be refused on revert too")
	}
}

// TestListRemoveAliases covers F11: every list takes ls, every remove takes rm.
func TestListRemoveAliases(t *testing.T) {
	_, env := agentsFlagFixture(t)
	mustRun(t, env, "mcp", "add", "srv", "--command", "echo")

	for _, args := range [][]string{
		{"mcp", "ls"},
		{"agent", "ls"},
		{"plugin", "ls"},
		{"marketplace", "ls"},
	} {
		if out, err := runCLI(t, env, args...); err != nil {
			t.Errorf("%v: %v\n%s", args, err, out)
		}
	}
	if out, err := runCLI(t, env, "mcp", "rm", "srv"); err != nil {
		t.Fatalf("mcp rm: %v\n%s", err, out)
	}
	if _, err := runCLI(t, env, "mcp", "rm", "srv"); err == nil {
		t.Fatal("`mcp rm` did not actually remove the server the first time")
	}
}
