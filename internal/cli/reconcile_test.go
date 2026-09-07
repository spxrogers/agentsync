package cli_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
)

// TestReconcile_OrphanFile is the regression/feature test for the maintainer
// decision that agentsync should PROMPT to delete or preserve an orphaned dest
// file (a whole-file dest agentsync owns but no source component renders
// anymore). [r]emove backs up then deletes + prunes state; [k]eep leaves it.
func TestReconcile_OrphanFile(t *testing.T) {
	setup := func(t *testing.T) (env map[string]string, dest string) {
		t.Helper()
		tmp := t.TempDir()
		env = map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
		mustRun(t, env, "init")
		mustRun(t, env, "agent", "add", "claude")
		// Two skills: demo becomes the orphan; keep stays in sync, so its
		// state entry must survive the orphan's prune — a wiped state file
		// would pass a "demo is gone" check for the wrong reason.
		for _, name := range []string{"demo", "keep"} {
			skill := filepath.Join(tmp, ".agentsync", "skills", name, "SKILL.md")
			_ = os.MkdirAll(filepath.Dir(skill), 0o755)
			_ = os.WriteFile(skill, []byte("---\nname: "+name+"\ndescription: d\n---\nbody\n"), 0o644)
		}
		mustRun(t, env, "apply")
		dest = filepath.Join(tmp, ".claude", "skills", "demo", "SKILL.md")
		if _, err := os.Stat(dest); err != nil {
			t.Fatalf("dest skill not written by apply: %v", err)
		}
		// Remove the source component → the dest is now an orphan.
		_ = os.RemoveAll(filepath.Join(tmp, ".agentsync", "skills", "demo"))
		return env, dest
	}

	t.Run("remove backs up and deletes", func(t *testing.T) {
		env, dest := setup(t)
		// apply recorded the dest in state.Files; [r] must prune that entry AND
		// the run must persist the prune, or the next apply still believes it
		// owns a file that is gone (issue #171). Measured before this assertion
		// existed: dropping the prune's stateDirty flag failed zero tests.
		root := env["AGENTSYNC_TARGET_ROOT"]
		keep := filepath.Join(root, ".claude", "skills", "keep", "SKILL.md")
		stateOwns := func(p string) bool {
			t.Helper()
			st, err := state.Load(filepath.Join(root, ".agentsync", ".state", "targets.json"))
			if err != nil {
				t.Fatalf("load state: %v", err)
			}
			portable := paths.HomeRelative(root, p)
			for key := range st.Files {
				if key.Path == portable {
					return true
				}
			}
			return false
		}
		if !stateOwns(dest) || !stateOwns(keep) {
			t.Fatal("precondition: apply should have recorded both skills in state.Files")
		}
		out, err := runCLIWithStdin(t, env, "r", "reconcile")
		if err != nil {
			t.Fatalf("reconcile: %v\n%s", err, out)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatalf("orphan dest should have been removed; stat err=%v\n%s", err, out)
		}
		if stateOwns(dest) {
			t.Fatalf("state still owns the removed orphan: the prune was not persisted\n%s", out)
		}
		if !stateOwns(keep) {
			t.Fatalf("the prune must be exact: the in-sync sibling's state entry is gone too\n%s", out)
		}
		// A backup of the removed file must exist.
		backups := filepath.Join(env["AGENTSYNC_TARGET_ROOT"], ".agentsync", ".state", "backups")
		found := false
		_ = filepath.Walk(backups, func(p string, fi os.FileInfo, e error) error {
			if e == nil && !fi.IsDir() && strings.HasSuffix(p, "SKILL.md") {
				found = true
			}
			return nil
		})
		if !found {
			t.Fatalf("expected a backup of the removed orphan under %s", backups)
		}
	})

	t.Run("keep preserves the file", func(t *testing.T) {
		env, dest := setup(t)
		if _, err := runCLIWithStdin(t, env, "k", "reconcile"); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if _, err := os.Stat(dest); err != nil {
			t.Fatalf("orphan dest should have been kept; stat err=%v", err)
		}
	})
}

func TestReconcile_NoDrift(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "reconcile", "--auto-safe"); err != nil {
		t.Fatal(err)
	}
}

func TestReconcile_AutoOverride(t *testing.T) {
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

	// Manually mutate destination to create drift.
	dst := filepath.Join(tmp, ".claude.json")
	body, _ := os.ReadFile(dst)
	drifted := strings.Replace(string(body), `"npx"`, `"npm"`, 1)
	_ = os.WriteFile(dst, []byte(drifted), 0o644)

	// reconcile --auto-override should re-apply source value.
	if _, err := runCLI(t, env, "reconcile", "--auto-override"); err != nil {
		t.Fatal(err)
	}
	final, _ := os.ReadFile(dst)
	if !strings.Contains(string(final), `"npx"`) {
		t.Fatalf("override didn't restore source value: %s", final)
	}
}

// TestReconcile_AutoWriteback_ForeignCollisionDoesNotClobberSource is the
// regression for the worst data-loss path: --auto-writeback mapped EVERY
// actionable item (including ForeignCollision — a never-applied pre-existing
// native file) to write-back, overwriting the curated source with whatever
// foreign content the dest happened to hold.
func TestReconcile_AutoWriteback_ForeignCollisionDoesNotClobberSource(t *testing.T) {
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

	// Pre-existing native file agentsync never applied (→ ForeignCollision).
	dst := filepath.Join(tmp, ".claude.json")
	_ = os.WriteFile(dst, []byte(`{"mcpServers":{"github":{"type":"stdio","command":"FOREIGN"}}}`), 0o644)

	if _, err := runCLI(t, env, "reconcile", "--auto-writeback"); err != nil {
		t.Fatalf("reconcile --auto-writeback: %v", err)
	}
	src, _ := os.ReadFile(mcp)
	if strings.Contains(string(src), "FOREIGN") || !strings.Contains(string(src), "npx") {
		t.Fatalf("auto-writeback clobbered curated source with foreign dest content:\n%s", src)
	}
}

// TestReconcile_Writeback_PreservesSourceOnlyFields is the regression for
// write-back reconstructing the source MCP entry purely from the dest spec
// (which never carries agents/enabled), silently dropping the user's
// targeting/enablement config.
func TestReconcile_Writeback_PreservesSourceOnlyFields(t *testing.T) {
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
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\nagents=[\"claude\"]\nenabled=true\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	// Drift the dest command so write-back rewrites the source.
	dst := filepath.Join(tmp, ".claude.json")
	body, _ := os.ReadFile(dst)
	_ = os.WriteFile(dst, []byte(strings.Replace(string(body), `"npx"`, `"npm"`, 1)), 0o644)

	if _, err := runCLI(t, env, "reconcile", "--auto-writeback"); err != nil {
		t.Fatalf("reconcile --auto-writeback: %v", err)
	}
	src, _ := os.ReadFile(mcp)
	if !strings.Contains(string(src), "npm") {
		t.Fatalf("write-back didn't capture the dest edit: %s", src)
	}
	if !strings.Contains(string(src), "agents") || !strings.Contains(string(src), "enabled") {
		t.Fatalf("write-back dropped source-only agents/enabled fields:\n%s", src)
	}
}

// TestReconcile_Writeback_OpenCodeMCP is the regression for opencode MCP
// write-back being dead: opencode renders MCP under the JSON key "mcp"
// (pointers /mcp/<id>), but writeBackKeyItem only matched the claude shape
// "mcpServers", so reconcile [w]rite-back of a drifted opencode MCP server
// always errored "only /mcpServers/* items can be written back" — the user
// could never persist an opencode MCP edit.
func TestReconcile_Writeback_OpenCodeMCP(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "opencode"); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\nagents=[\"opencode\"]\nenabled=true\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	// Drift the opencode dest command so write-back must rewrite the source.
	dst := filepath.Join(tmp, ".config", "opencode", "opencode.json")
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read opencode dest: %v", err)
	}
	if !strings.Contains(string(body), `"npx"`) {
		t.Fatalf("opencode dest missing expected mcp command:\n%s", body)
	}
	_ = os.WriteFile(dst, []byte(strings.Replace(string(body), `"npx"`, `"npm"`, 1)), 0o644)

	if _, err := runCLI(t, env, "reconcile", "--auto-writeback"); err != nil {
		t.Fatalf("reconcile --auto-writeback (opencode mcp): %v", err)
	}
	src, _ := os.ReadFile(mcp)
	if !strings.Contains(string(src), "npm") {
		t.Fatalf("opencode mcp write-back didn't capture the dest edit:\n%s", src)
	}
	if !strings.Contains(string(src), "agents") || !strings.Contains(string(src), "enabled") {
		t.Fatalf("opencode mcp write-back dropped source-only agents/enabled fields:\n%s", src)
	}
}

// TestReconcile_AutoFlagsMutuallyExclusive is the regression for both
// --auto-writeback and --auto-override being silently accepted (writeback
// wins) despite being exact opposites.
func TestReconcile_AutoFlagsMutuallyExclusive(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "reconcile", "--auto-writeback", "--auto-override")
	if err == nil {
		t.Fatal("expected error when both --auto-writeback and --auto-override are set")
	}
}

func TestReconcile_AutoSafe_NoDriftItems(t *testing.T) {
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

	// No drift: auto-safe should exit 0 and say "nothing to reconcile".
	out, err := runCLI(t, env, "reconcile", "--auto-safe")
	if err != nil {
		t.Fatalf("reconcile --auto-safe: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to reconcile") {
		t.Fatalf("expected 'nothing to reconcile'; got: %s", out)
	}
}

func TestReconcile_InteractiveSkip(t *testing.T) {
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

	// Drift the destination.
	dst := filepath.Join(tmp, ".claude.json")
	body, _ := os.ReadFile(dst)
	_ = os.WriteFile(dst, []byte(strings.Replace(string(body), `"npx"`, `"npm"`, 1)), 0o644)

	// Run with scripted "s\n" input (skip) via runCLIWithStdin.
	out, err := runCLIWithStdin(t, env, "s\n", "reconcile")
	if err != nil {
		t.Fatalf("reconcile interactive skip: %v\n%s", err, out)
	}
	// Destination should be unchanged (still npm).
	final, _ := os.ReadFile(dst)
	if !strings.Contains(string(final), `"npm"`) {
		t.Fatalf("skip should leave dest unchanged; got: %s", final)
	}
}

// TestReconcile_BulkActionRequiresConfirmation is the regression test for
// the bug where a capital W/O/S immediately locked in the bulk choice for
// every remaining item with no preview and no chance to cancel. Combined
// with the writeback-no-op bug below, one accidental shift-W on a hook
// item could silently destroy edits across the whole queue.
func TestReconcile_BulkActionRequiresConfirmation(t *testing.T) {
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
	dst := filepath.Join(tmp, ".claude.json")
	body, _ := os.ReadFile(dst)
	_ = os.WriteFile(dst, []byte(strings.Replace(string(body), `"npx"`, `"npm"`, 1)), 0o644)

	// Press capital S (bulk skip) but decline confirmation. The reconcile
	// loop should NOT lock in the bulk choice; it should re-prompt the
	// item and wait for the lowercase per-item choice. We follow up with
	// 's' (skip just this one).
	out, err := runCLIWithStdin(t, env, "Sns\n", "reconcile")
	if err != nil {
		t.Fatalf("reconcile bulk-confirm decline: %v\n%s", err, out)
	}
	if !strings.Contains(out, "apply 's' to all") {
		t.Fatalf("expected bulk confirmation prompt; got: %s", out)
	}
	if !strings.Contains(out, "cancelled") {
		t.Fatalf("expected 'cancelled' message after declining confirm; got: %s", out)
	}
}

// TestReconcile_WriteBackUnsupportedReturnsError verifies that write-back
// for a pointer shape we cannot handle (e.g. /hooks/PreToolUse/0) returns
// an error visible to the user instead of silently printing
// "write-back: <label>" success — which used to mask data loss.
func TestReconcile_WriteBackUnsupportedReturnsError(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Author a hook source that produces a /hooks/* pointer in
	// .claude.json. The hook payload is irrelevant — we just need a
	// non-MCP key-level item to flow through reconcile.
	hookDir := filepath.Join(tmp, ".agentsync", "hooks")
	_ = os.MkdirAll(hookDir, 0o755)
	_ = os.WriteFile(filepath.Join(hookDir, "PreToolUse.toml"),
		[]byte("[[hook]]\nmatcher = \"*\"\ntype = \"command\"\ncommand = \"echo pre\"\n"),
		0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(tmp, ".claude", "settings.json")
	body, _ := os.ReadFile(dst)
	// Drift the hook in the destination so reconcile classifies it as Drift.
	_ = os.WriteFile(dst, []byte(strings.Replace(string(body), `echo pre`, `echo edited`, 1)), 0o644)

	// Press w (write-back this item, single).
	out, err := runCLIWithStdin(t, env, "w\n", "reconcile")
	// Should NOT silently print success for the hook write-back.
	// Tie the label to THIS message: a bare Contains(out, "ERROR") would be
	// satisfied by any unrelated ERROR elsewhere in the transcript, so a genuine
	// silent-success regression could hide behind one.
	wantErr := ui.LevelError.Label(ui.New(io.Discard, io.Discard, ui.ColorNever)) + "  write-back:"
	if strings.Contains(out, "write-back: ") && !strings.Contains(out, wantErr) {
		t.Fatalf("hook write-back must surface a labeled write-back error, not silent success; got:\n%s", out)
	}
	// And it must exit non-zero — a failed write-back did not persist the edit.
	if err == nil {
		t.Fatalf("interactive write-back of an unsupported item must exit non-zero; got nil err, out:\n%s", out)
	}
}

// TestReconcile_AutoWritebackFailureExitsNonZero is the regression for a silent
// failure on the SCRIPTABLE path: `reconcile --auto-writeback` printed a
// "write-back error" line but still exited 0, so `reconcile --auto-writeback &&
// deploy` proceeded as if the dest edit had been captured (the next apply would
// then clobber it). A failed write-back must exit non-zero.
func TestReconcile_AutoWritebackFailureExitsNonZero(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	hookDir := filepath.Join(tmp, ".agentsync", "hooks")
	_ = os.MkdirAll(hookDir, 0o755)
	_ = os.WriteFile(filepath.Join(hookDir, "PreToolUse.toml"),
		[]byte("[[hook]]\nmatcher = \"*\"\ntype = \"command\"\ncommand = \"echo pre\"\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(tmp, ".claude", "settings.json")
	body, _ := os.ReadFile(dst)
	_ = os.WriteFile(dst, []byte(strings.Replace(string(body), `echo pre`, `echo edited`, 1)), 0o644)

	if _, err := runCLI(t, env, "reconcile", "--auto-writeback"); err == nil {
		t.Fatal("reconcile --auto-writeback with a failed write-back must exit non-zero")
	}
}

// TestReconcile_SharedMCPDivergentWriteBackConflicts is the regression for a
// silent last-writer-wins data loss: an MCP server fanned out to two agents
// (agents=["*"]) edited DIFFERENTLY in each native config produced two
// write-back items targeting one source file; the second silently clobbered the
// first and left the first agent stuck in conflict. The run must now detect the
// divergence, keep the first write, refuse the second, and exit non-zero.
func TestReconcile_SharedMCPDivergentWriteBackConflicts(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	for _, a := range [][]string{{"init"}, {"agent", "add", "claude"}, {"agent", "add", "opencode"}} {
		if _, err := runCLI(t, env, a...); err != nil {
			t.Fatalf("%v: %v", a, err)
		}
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "shared.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"orig\"\nagents=[\"*\"]\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	// Divergent edits in each agent's native config.
	claudeDest := filepath.Join(tmp, ".claude.json")
	ocDest := filepath.Join(tmp, ".config", "opencode", "opencode.json")
	cb, _ := os.ReadFile(claudeDest)
	_ = os.WriteFile(claudeDest, []byte(strings.Replace(string(cb), "orig", "CLAUDE_EDIT", 1)), 0o644)
	ob, _ := os.ReadFile(ocDest)
	_ = os.WriteFile(ocDest, []byte(strings.Replace(string(ob), "orig", "OC_EDIT", 1)), 0o644)

	out, err := runCLI(t, env, "reconcile", "--auto-writeback")
	if err == nil {
		t.Fatalf("divergent shared-MCP write-back must NOT silently succeed; out:\n%s", out)
	}
	if !strings.Contains(out, "conflict") {
		t.Fatalf("expected a conflict report; got:\n%s", out)
	}
	// Source kept exactly ONE consistent value (the first writer), not silently
	// the second; and not a half-merged mess.
	src, _ := os.ReadFile(mcp)
	hasClaude := strings.Contains(string(src), "CLAUDE_EDIT")
	hasOC := strings.Contains(string(src), "OC_EDIT")
	if hasClaude == hasOC { // both or neither
		t.Fatalf("source must hold exactly one writer's value after a conflict; got:\n%s", src)
	}
}

// TestReconcile_SharedMCPIdenticalWriteBackOK proves the conflict guard does NOT
// false-fire when both agents drifted the shared server to the SAME value.
func TestReconcile_SharedMCPIdenticalWriteBackOK(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	for _, a := range [][]string{{"init"}, {"agent", "add", "claude"}, {"agent", "add", "opencode"}} {
		if _, err := runCLI(t, env, a...); err != nil {
			t.Fatalf("%v: %v", a, err)
		}
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "shared.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"orig\"\nagents=[\"*\"]\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{filepath.Join(tmp, ".claude.json"), filepath.Join(tmp, ".config", "opencode", "opencode.json")} {
		b, _ := os.ReadFile(dest)
		_ = os.WriteFile(dest, []byte(strings.Replace(string(b), "orig", "SAME_EDIT", 1)), 0o644)
	}
	if _, err := runCLI(t, env, "reconcile", "--auto-writeback"); err != nil {
		t.Fatalf("identical shared-MCP edits must not conflict: %v", err)
	}
	if src, _ := os.ReadFile(mcp); !strings.Contains(string(src), "SAME_EDIT") {
		t.Fatalf("expected SAME_EDIT captured; got:\n%s", src)
	}
}

func TestReconcile_InteractiveQuit(t *testing.T) {
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

	// Drift the destination.
	dst := filepath.Join(tmp, ".claude.json")
	body, _ := os.ReadFile(dst)
	_ = os.WriteFile(dst, []byte(strings.Replace(string(body), `"npx"`, `"npm"`, 1)), 0o644)

	// Quit immediately.
	out, err := runCLIWithStdin(t, env, "q\n", "reconcile")
	if err != nil {
		t.Fatalf("reconcile quit: %v\n%s", err, out)
	}
	if !strings.Contains(out, "quit") {
		t.Fatalf("expected 'quit' in output; got: %s", out)
	}
}

// TestReconcile_EOFFlushesOverrides is the regression for the interactive EOF path
// dropping queued state (issue #171): after choosing [o]verride on a drift item, an
// EOF (piped input ends) must still reach `done:` so the queued override is applied
// (a bare `return nil` used to drop it).
func TestReconcile_EOFFlushesOverrides(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun := func(args ...string) {
		t.Helper()
		if _, err := runCLI(t, env, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	mustRun("init")
	mustRun("agent", "add", "claude")
	for _, name := range []string{"aaa", "bbb"} {
		mcp := filepath.Join(tmp, ".agentsync", "mcp", name+".toml")
		_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
		_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)
	}
	mustRun("apply")

	// Drift BOTH servers in the destination (npx -> npm).
	dst := filepath.Join(tmp, ".claude.json")
	body, _ := os.ReadFile(dst)
	if err := os.WriteFile(dst, []byte(strings.ReplaceAll(string(body), `"npx"`, `"npm"`)), 0o644); err != nil {
		t.Fatal(err)
	}

	// A single 'o' overrides the first drift item; the stream then EOFs before the
	// second is answered. Post-fix, EOF reaches `done:` and applies the queued
	// override (restoring the source "npx" for the first item).
	out, err := runCLIWithStdin(t, env, "o", "reconcile")
	if err != nil {
		t.Fatalf("reconcile: %v\n%s", err, out)
	}
	final, _ := os.ReadFile(dst)
	if !strings.Contains(string(final), `"npx"`) {
		t.Fatalf("EOF after [o]verride dropped the queued override (source value not restored):\n%s", final)
	}
}

// TestReconcile_FinishRunsExactlyOnce is the behavioural half of the #232
// decision that the run's tail (finish) has exactly ONE call site and is never
// deferred. finish prints one summary line per run — "N item(s) left
// unresolved" in an auto mode, "override: applied N item(s)" after an
// [o]verride — so the observable trace of a finish that ran twice (a defer plus
// the explicit call, or a second call added later) is exactly a duplicated line.
//
// Three exits are covered: the natural end of an --auto-safe pass, an EOF after
// [o]verride, and [q]uit after [o]verride. The last one also pins that quitting
// still APPLIES the queued override: both quits reach finish exactly as the
// EOFs do (TestReconcile_EOFFlushesOverrides), but no test or scripted scenario
// ever queued an override and then quit, so a quit that dropped the queue
// failed nothing.
func TestReconcile_FinishRunsExactlyOnce(t *testing.T) {
	setup := func(t *testing.T) (env map[string]string, dst string) {
		t.Helper()
		tmp := t.TempDir()
		env = map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
		mustRun(t, env, "init")
		mustRun(t, env, "agent", "add", "claude")
		for _, name := range []string{"aaa", "bbb"} {
			mcp := filepath.Join(tmp, ".agentsync", "mcp", name+".toml")
			_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
			_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)
		}
		mustRun(t, env, "apply")
		// Drift BOTH servers in the destination (npx -> npm).
		dst = filepath.Join(tmp, ".claude.json")
		body, _ := os.ReadFile(dst)
		if err := os.WriteFile(dst, []byte(strings.ReplaceAll(string(body), `"npx"`, `"npm"`)), 0o644); err != nil {
			t.Fatal(err)
		}
		return env, dst
	}
	// commandOf reads mcpServers.<id>.command from the claude destination.
	commandOf := func(t *testing.T, dst, id string) string {
		t.Helper()
		var doc struct {
			MCPServers map[string]struct {
				Command string `json:"command"`
			} `json:"mcpServers"`
		}
		body, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("parse %s: %v\n%s", dst, err, body)
		}
		return doc.MCPServers[id].Command
	}

	t.Run("auto-safe prints the unresolved summary once", func(t *testing.T) {
		env, _ := setup(t)
		out, err := runCLI(t, env, "reconcile", "--auto-safe")
		if err != nil {
			t.Fatalf("reconcile --auto-safe: %v\n%s", err, out)
		}
		if n := strings.Count(out, "item(s) left unresolved"); n != 1 {
			t.Fatalf("the unresolved summary must be printed exactly once (finish ran %d times):\n%s", n, out)
		}
		if !strings.Contains(out, "2 item(s) left unresolved") {
			t.Fatalf("both drifted servers should be counted; got:\n%s", out)
		}
	})

	t.Run("EOF after override applies the queue once", func(t *testing.T) {
		env, dst := setup(t)
		out, err := runCLIWithStdin(t, env, "o", "reconcile")
		if err != nil {
			t.Fatalf("reconcile: %v\n%s", err, out)
		}
		if n := strings.Count(out, "override: applied"); n != 1 {
			t.Fatalf("the override summary must be printed exactly once (finish ran %d times):\n%s", n, out)
		}
		if got := commandOf(t, dst, "aaa"); got != "npx" {
			t.Fatalf("the queued override was not applied at EOF: aaa.command = %q, want npx", got)
		}
	})

	t.Run("quit after override still applies the queue once", func(t *testing.T) {
		env, dst := setup(t)
		out, err := runCLIWithStdin(t, env, "oq", "reconcile")
		if err != nil {
			t.Fatalf("reconcile: %v\n%s", err, out)
		}
		if !strings.Contains(out, "quit") {
			t.Fatalf("expected the [q]uit echo; got:\n%s", out)
		}
		if n := strings.Count(out, "override: applied 1 item(s)"); n != 1 {
			t.Fatalf("[q]uit must still apply the ONE queued override, exactly once (got %d summary lines):\n%s", n, out)
		}
		// Only the first server is asserted: the queued op is the whole merge
		// file's op (which is why dedupOverride keys by path), so the re-apply
		// restores every owned key in .claude.json, not just aaa.
		if got := commandOf(t, dst, "aaa"); got != "npx" {
			t.Fatalf("[q]uit dropped the queued override: aaa.command = %q, want npx (restored from source)", got)
		}
	})
}

// TestReconcile_ProjectScope_OverrideRecordsProjectState pins the scope and
// project root that finish hands to render.NewWriter and RecordOpsState. Every
// other reconcile test runs at user scope, where a transposition to
// ScopeUser/"" is invisible; at project scope it makes the writer look the
// destination up under the wrong state key, treat the project's own .mcp.json
// as a never-applied foreign file, back it up and print a "backup:" line, and
// record the re-apply against the wrong scope.
func TestReconcile_ProjectScope_OverrideRecordsProjectState(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "init", "--scope", "project", "--project", projectDir)
	declareProjectAgent(t, env, projectDir, "claude")
	scaffoldProjectMCP(t, projectDir, "github", "npx")
	mustRun(t, env, "apply", "--project", projectDir)

	mcpPath := filepath.Join(projectDir, ".mcp.json")
	body, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read project .mcp.json: %v", err)
	}
	drifted := strings.ReplaceAll(string(body), `"npx"`, `"npm"`)
	if drifted == string(body) {
		t.Fatalf("fixture did not contain the expected command to drift:\n%s", body)
	}
	if err := os.WriteFile(mcpPath, []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLIWithStdin(t, env, "o", "reconcile", "--project", projectDir)
	if err != nil {
		t.Fatalf("reconcile --project: %v\n%s", err, out)
	}
	if !strings.Contains(out, "override: applied 1 item(s)") {
		t.Fatalf("expected the override to be applied; got:\n%s", out)
	}
	if strings.Contains(out, "backup:") {
		t.Fatalf("the project's own destination was treated as a foreign collision (wrong scope in finish):\n%s", out)
	}
	if final, _ := os.ReadFile(mcpPath); !strings.Contains(string(final), `"npx"`) {
		t.Fatalf("override did not restore the project source value:\n%s", final)
	}
	for _, backups := range []string{
		filepath.Join(tmpHome, ".agentsync", ".state", "backups"),
		filepath.Join(projectDir, ".agentsync", ".state", "backups"),
	} {
		if _, err := os.Stat(backups); !os.IsNotExist(err) {
			t.Fatalf("no backup may be taken for an owned project destination; found %s (stat err=%v)", backups, err)
		}
	}
	// The re-apply must be recorded against the project's state, so the tree
	// reads clean at project scope afterwards.
	if out, err := runCLI(t, env, "status", "--project", projectDir, "--exit-code"); err != nil {
		t.Fatalf("status --project after override should be clean: %v\n%s", err, out)
	}
}

// TestReconcile_DroppedServer_WriteBackRemovesSource pins the deletion-only
// exception to the capture funnel, removeDroppedSource, on each of the three
// routes the secret-handling docs name for it: a per-item [w], a confirmed
// bulk [W], and --auto-writeback. Each must unlink the canonical mcp/<id>.toml
// of the server the destination dropped, while the drifted sibling is written
// back normally. Before this test the function had no in-repo coverage at all;
// only the out-of-tree scripted-stdin harness reached it.
func TestReconcile_DroppedServer_WriteBackRemovesSource(t *testing.T) {
	setup := func(t *testing.T) (env map[string]string, srcDir string) {
		t.Helper()
		tmp := t.TempDir()
		env = map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
		mustRun(t, env, "init")
		mustRun(t, env, "agent", "add", "claude")
		srcDir = filepath.Join(tmp, ".agentsync", "mcp")
		_ = os.MkdirAll(srcDir, 0o755)
		for _, name := range []string{"dropped", "kept"} {
			_ = os.WriteFile(filepath.Join(srcDir, name+".toml"), []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)
		}
		mustRun(t, env, "apply")
		// The destination drops one server outright and drifts the other.
		dst := filepath.Join(tmp, ".claude.json")
		body, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("parse %s: %v\n%s", dst, err, body)
		}
		servers, _ := doc["mcpServers"].(map[string]any)
		if servers == nil || servers["dropped"] == nil || servers["kept"] == nil {
			t.Fatalf("apply did not render both servers into %s:\n%s", dst, body)
		}
		delete(servers, "dropped")
		servers["kept"].(map[string]any)["command"] = "npm"
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, out, 0o644); err != nil {
			t.Fatal(err)
		}
		return env, srcDir
	}

	tests := []struct {
		name  string
		stdin string
		args  []string
		want  string // a transcript line unique to the route
	}{
		{name: "per-item [w]", stdin: "ww", want: "  > w\n"},
		{name: "confirmed bulk [W]", stdin: "Wy", want: "apply 'w' to all 2 remaining items? [y/N] y\n"},
		{name: "--auto-writeback", stdin: "", args: []string{"--auto-writeback"}, want: "write-back: "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, srcDir := setup(t)
			out, err := runCLIWithStdin(t, env, tc.stdin, append([]string{"reconcile"}, tc.args...)...)
			if err != nil {
				t.Fatalf("reconcile: %v\n%s", err, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("transcript should show the %s route; want %q in:\n%s", tc.name, tc.want, out)
			}
			if !strings.Contains(out, "write-back: removed source mcp/dropped.toml (destination dropped ") {
				t.Errorf("the dropped server's source removal must be reported; transcript:\n%s", out)
			}
			if _, err := os.Stat(filepath.Join(srcDir, "dropped.toml")); !os.IsNotExist(err) {
				t.Errorf("mcp/dropped.toml should have been unlinked; stat err = %v\n%s", err, out)
			}
			kept, err := os.ReadFile(filepath.Join(srcDir, "kept.toml"))
			if err != nil {
				t.Fatalf("mcp/kept.toml must survive: %v", err)
			}
			if !strings.Contains(string(kept), "npm") {
				t.Errorf("the drifted sibling should have been written back to npm; kept.toml:\n%s", kept)
			}
		})
	}
}
