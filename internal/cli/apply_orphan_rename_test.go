package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApply_SubagentAndCommandOrphanCleanup pins the reclamation that issue #211
// required apply to grow. Before it, only "skills/" SourceIDs were reclaimed:
// PruneStaleState dropped the state entry for a subagent or command that stopped
// being rendered, but the destination file itself lingered forever.
//
// That mattered because namespacing plugin-provided components RENAMES every one
// of them exactly once, on the first apply after upgrading — and Claude Code
// reads every file in its agents directory, so a stale pre-rename file left
// beside the new namespaced one would leave the user with MORE duplicate agents
// than before, not fewer. Claude's own docs are explicit that two same-name
// definitions in one directory mean it loads only one, "chosen by filesystem read
// order rather than a documented precedence".
//
// The same convergence argument covers the ordinary case exercised here: a
// component removed from (or renamed in) the canonical source.
func TestApply_SubagentAndCommandOrphanCleanup(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	srcSubagent := filepath.Join(tmp, ".agentsync", "subagents", "reviewer.md")
	srcCommand := filepath.Join(tmp, ".agentsync", "commands", "review.md")
	destSubagent := filepath.Join(tmp, ".claude", "agents", "reviewer.md")
	destCommand := filepath.Join(tmp, ".claude", "commands", "review.md")
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	if err := os.WriteFile(srcSubagent, []byte("---\nname: reviewer\n---\nreview it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcCommand, []byte("---\ndescription: d\n---\nreview it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 1: %v\n%s", err, out)
	}
	if !exists(destSubagent) || !exists(destCommand) {
		t.Fatal("apply 1 did not write the subagent and command")
	}

	// Remove both from source. apply must reclaim the destination files rather
	// than leave them behind as components the agent still loads.
	if err := os.Remove(srcSubagent); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(srcCommand); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 2: %v\n%s", err, out)
	}
	if exists(destSubagent) {
		t.Fatalf("orphaned subagent %s was not reclaimed", destSubagent)
	}
	if exists(destCommand) {
		t.Fatalf("orphaned command %s was not reclaimed", destCommand)
	}
	// The directories themselves must survive — unlike a skill (which IS a
	// directory), these are flat files in a directory the agent always owns.
	if !exists(filepath.Join(tmp, ".claude", "agents")) || !exists(filepath.Join(tmp, ".claude", "commands")) {
		t.Fatal("the agents/commands roots must not be pruned")
	}
}

// TestApply_OrphanedSubagentDriftIsBackedUp holds the line that reclamation must
// not cross: an orphan delete can remove agentsync's own output freely, but a
// destination the user hand-edited since the last apply is unsynced content, and
// the write path's never-destroy-unsynced-content invariant applies to deletes
// too. Extending reclamation from skills to subagents and commands extends this
// guarantee with it rather than opening a hole.
func TestApply_OrphanedSubagentDriftIsBackedUp(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	srcSubagent := filepath.Join(tmp, ".agentsync", "subagents", "reviewer.md")
	destSubagent := filepath.Join(tmp, ".claude", "agents", "reviewer.md")
	if err := os.WriteFile(srcSubagent, []byte("---\nname: reviewer\n---\nreview it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 1: %v\n%s", err, out)
	}

	// Hand-edit the destination, then remove the component from source.
	if err := os.WriteFile(destSubagent, []byte("---\nname: reviewer\n---\nMY UNSYNCED EDIT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(srcSubagent); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 2: %v\n%s", err, out)
	}
	if _, err := os.Stat(destSubagent); err == nil {
		t.Fatal("the orphaned subagent should still be reclaimed")
	}

	// The hand-edit must survive somewhere under the backup root.
	backups := filepath.Join(tmp, ".agentsync", ".state", "backups")
	var found bool
	err := filepath.Walk(backups, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // a missing backup root is reported by !found below
		}
		data, rerr := os.ReadFile(p)
		if rerr == nil && strings.Contains(string(data), "MY UNSYNCED EDIT") {
			found = true
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk backups: %v", err)
	}
	if !found {
		t.Fatal("an orphan delete must back up a drifted destination before removing it")
	}
}
