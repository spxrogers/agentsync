package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		skill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
		_ = os.MkdirAll(filepath.Dir(skill), 0o755)
		_ = os.WriteFile(skill, []byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644)
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
		out, err := runCLIWithStdin(t, env, "r", "reconcile")
		if err != nil {
			t.Fatalf("reconcile: %v\n%s", err, out)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatalf("orphan dest should have been removed; stat err=%v\n%s", err, out)
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
	if strings.Contains(out, "write-back: ") && !strings.Contains(out, "write-back error") {
		t.Fatalf("hook write-back must surface an error, not silent success; got:\n%s", out)
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
