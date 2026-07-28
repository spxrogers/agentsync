package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The acceptance suite for the retired `agents/` → `subagents/` canonical
// layout (#200 F1). Every assertion is anchored to the ON-DISK result — the
// files, the state file's recorded SourceIDs, the directory that must be gone
// — not to a parsed model, per CLAUDE.md's models-stay-faithful rule.

const migTestSubagent = "---\ndescription: reviewer\n---\nReview carefully.\n"

// seedLegacySubagent writes <home>/.agentsync/agents/<name>.md, the pre-rename
// spelling a real user's committed dotfiles repo would carry.
func seedLegacySubagent(t *testing.T, agentsyncHome, name, body string) string {
	t.Helper()
	dir := filepath.Join(agentsyncHome, "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// readStateSourceIDs returns every FileEntry SourceID in the central state file,
// keyed by its full state key, so a test can assert WHICH tree's entries were
// rewritten.
func readStateSourceIDs(t *testing.T, agentsyncHome string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(agentsyncHome, ".state", "targets.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var doc struct {
		Files map[string]struct {
			SourceID string `json:"source_id"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	out := map[string]string{}
	for k, v := range doc.Files {
		out[k] = v.SourceID
	}
	return out
}

// TestMigrateSubagents_MovesFilesAndRewritesState is case (1): a legacy tree
// whose state carries old-spelling SourceIDs migrates to byte-identical files
// under subagents/, the recorded SourceIDs are rewritten, and agents/ is gone.
func TestMigrateSubagents_MovesFilesAndRewritesState(t *testing.T) {
	tmpHome := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}
	agentsyncHome := filepath.Join(tmpHome, ".agentsync")

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Apply once with the CURRENT spelling so state records a real subagent
	// entry, then rename the directory back to the legacy spelling — the exact
	// on-disk shape a user upgrading across the rename presents.
	newDir := filepath.Join(agentsyncHome, "subagents")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "reviewer.md"), []byte(migTestSubagent), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if err := os.Rename(newDir, filepath.Join(agentsyncHome, "agents")); err != nil {
		t.Fatal(err)
	}
	// Rewrite the recorded SourceIDs to the legacy spelling to complete the
	// pre-migration picture.
	statePath := filepath.Join(agentsyncHome, ".state", "targets.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(strings.ReplaceAll(string(raw), "subagents/", "agents/")), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "migrate", "subagents")
	if err != nil {
		t.Fatalf("migrate subagents: %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(newDir, "reviewer.md"))
	if err != nil {
		t.Fatalf("migrated file missing: %v", err)
	}
	if string(got) != migTestSubagent {
		t.Fatalf("migrated file is not byte-identical:\ngot  %q\nwant %q", got, migTestSubagent)
	}
	if _, err := os.Stat(filepath.Join(agentsyncHome, "agents")); !os.IsNotExist(err) {
		t.Fatalf("legacy agents/ dir still present after migration (err=%v)", err)
	}
	ids := readStateSourceIDs(t, agentsyncHome)
	found := false
	for key, id := range ids {
		if strings.HasPrefix(filepath.ToSlash(id), "agents/") {
			t.Fatalf("state key %q still carries the legacy SourceID %q", key, id)
		}
		if filepath.ToSlash(id) == "subagents/reviewer.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no state entry was rewritten to subagents/reviewer.md; got %v", ids)
	}

	// The migrated tree loads and applies cleanly (and, crucially, does not
	// disown the already-rendered subagent).
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply after migration: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(tmpHome, ".claude", "agents", "reviewer.md")); err != nil {
		t.Fatalf("rendered subagent was disowned by the post-migration apply: %v", err)
	}
}

// TestMigrateSubagents_SecondRunIsNoOp is case (2).
func TestMigrateSubagents_SecondRunIsNoOp(t *testing.T) {
	tmpHome := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}
	agentsyncHome := filepath.Join(tmpHome, ".agentsync")

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	seedLegacySubagent(t, agentsyncHome, "reviewer", migTestSubagent)

	if out, err := runCLI(t, env, "migrate", "subagents"); err != nil {
		t.Fatalf("first migrate: %v\n%s", err, out)
	}
	out, err := runCLI(t, env, "migrate", "subagents")
	if err != nil {
		t.Fatalf("second migrate must be a clean no-op: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to migrate") {
		t.Fatalf("second run did not report a no-op:\n%s", out)
	}
	got, err := os.ReadFile(filepath.Join(agentsyncHome, "subagents", "reviewer.md"))
	if err != nil || string(got) != migTestSubagent {
		t.Fatalf("second run disturbed the migrated file: %v / %q", err, got)
	}
}

// TestMigrateSubagents_NoInputErrorNamesTheCommand is case (3): with no TTY (or
// --no-input) a source-loading command must hard-error and name the one command
// to run, rather than prompting or proceeding.
func TestMigrateSubagents_NoInputErrorNamesTheCommand(t *testing.T) {
	tmpHome := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}
	agentsyncHome := filepath.Join(tmpHome, ".agentsync")

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	seedLegacySubagent(t, agentsyncHome, "reviewer", migTestSubagent)

	out, err := runCLI(t, env, "status", "--no-input")
	if err == nil {
		t.Fatalf("status must refuse an unmigrated tree; got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "agentsync migrate subagents") {
		t.Fatalf("error does not name the migration command: %v", err)
	}
}

// TestMigrateSubagents_LoadingCommandsRefusePreMigration is case (5): the
// disown hazard. apply/import/reconcile must all refuse, not silently render a
// zero-subagent plan.
func TestMigrateSubagents_LoadingCommandsRefusePreMigration(t *testing.T) {
	tmpHome := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}
	agentsyncHome := filepath.Join(tmpHome, ".agentsync")

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	seedLegacySubagent(t, agentsyncHome, "reviewer", migTestSubagent)

	for _, args := range [][]string{
		{"apply", "--no-input"},
		{"apply", "--dry-run", "--no-input"},
		{"import", "claude", "--dry-run", "--no-input"},
		{"reconcile", "--no-input"},
		{"diff", "--no-input"},
	} {
		out, err := runCLI(t, env, args...)
		if err == nil {
			t.Fatalf("%v must refuse an unmigrated tree; got success:\n%s", args, out)
		}
		if !strings.Contains(err.Error(), "migrate subagents") {
			t.Fatalf("%v error is not the migration-pending error: %v", args, err)
		}
	}
}

// TestMigrateSubagents_BothDirsCollisionRefused is case (6): a hand-started
// migration that left the same name on both sides is refused with the colliding
// files listed, and NOTHING is moved.
func TestMigrateSubagents_BothDirsCollisionRefused(t *testing.T) {
	tmpHome := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}
	agentsyncHome := filepath.Join(tmpHome, ".agentsync")

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	seedLegacySubagent(t, agentsyncHome, "reviewer", migTestSubagent)
	seedLegacySubagent(t, agentsyncHome, "planner", "planner legacy\n")
	newDir := filepath.Join(agentsyncHome, "subagents")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const handMoved = "hand-moved copy\n"
	if err := os.WriteFile(filepath.Join(newDir, "reviewer.md"), []byte(handMoved), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "migrate", "subagents")
	if err == nil {
		t.Fatalf("colliding migration must be refused; got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "reviewer.md") {
		t.Fatalf("refusal does not list the colliding file: %v", err)
	}
	// Nothing moved: both legacy files are still where they were, and the
	// hand-moved copy is untouched.
	for _, name := range []string{"reviewer.md", "planner.md"} {
		if _, statErr := os.Stat(filepath.Join(agentsyncHome, "agents", name)); statErr != nil {
			t.Fatalf("refused migration moved %s anyway: %v", name, statErr)
		}
	}
	got, rerr := os.ReadFile(filepath.Join(newDir, "reviewer.md"))
	if rerr != nil || string(got) != handMoved {
		t.Fatalf("refused migration overwrote the hand-moved copy: %v / %q", rerr, got)
	}
}

// TestMigrateSubagents_ProjectScopeIsIndependent is case (4): a project tree
// migrates on its own, and the state rewrite touches only that tree's keys —
// another project's (and the user tree's) recorded SourceIDs are left alone.
func TestMigrateSubagents_ProjectScopeIsIndependent(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}
	agentsyncHome := filepath.Join(tmpHome, ".agentsync")

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", proj); err != nil {
		t.Fatal(err)
	}
	declareProjectAgent(t, env, proj, "claude")

	// A user-scope subagent, already applied and recorded — the bystander whose
	// state entry a project-scope migration must not touch.
	userSub := filepath.Join(agentsyncHome, "subagents")
	if err := os.MkdirAll(userSub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userSub, "userone.md"), []byte(migTestSubagent), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply", "--scope", "user"); err != nil {
		t.Fatalf("user apply: %v\n%s", err, out)
	}
	// Fake a stale legacy SourceID on the USER entry so we can prove the
	// project-scope migration leaves it alone.
	statePath := filepath.Join(agentsyncHome, ".state", "targets.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(strings.ReplaceAll(string(raw), "subagents/userone.md", "agents/userone.md")), 0o644); err != nil {
		t.Fatal(err)
	}

	projHome := filepath.Join(proj, ".agentsync")
	seedLegacySubagent(t, projHome, "projone", "project subagent\n")

	out, err := runCLI(t, env, "migrate", "subagents", "--project", proj)
	if err != nil {
		t.Fatalf("project migrate: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(projHome, "subagents", "projone.md")); err != nil {
		t.Fatalf("project subagent not migrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projHome, "agents")); !os.IsNotExist(err) {
		t.Fatalf("project legacy dir still present: %v", err)
	}
	// The user tree is untouched — both its files and its (deliberately stale)
	// state entry.
	if _, err := os.Stat(filepath.Join(userSub, "userone.md")); err != nil {
		t.Fatalf("project migration disturbed the user tree: %v", err)
	}
	stale := false
	for _, id := range readStateSourceIDs(t, agentsyncHome) {
		if filepath.ToSlash(id) == "agents/userone.md" {
			stale = true
		}
	}
	if !stale {
		t.Fatal("project-scope migration rewrote a USER-scope state entry; the rewrite must be scoped to the migrated tree")
	}
}

// TestDoctor_ReportsPendingSubagentMigration pins the non-fatal surface for a
// pending migration — and the entire justification for source.LoadTolerant
// existing at all.
//
// Every other source-loading command is fail-closed on this condition, so
// `doctor` is the one place a stuck user can still get a diagnosis. If it ever
// starts dying at load instead, the fail-closed gate becomes a dead end with no
// way out but reading the source.
func TestDoctor_ReportsPendingSubagentMigration(t *testing.T) {
	tmpHome := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	seedLegacySubagent(t, filepath.Join(tmpHome, ".agentsync"), "reviewer", migTestSubagent)

	out, _ := runCLI(t, env, "doctor")
	// doctor exits non-zero when a check fails; the diagnosis is the point.
	if !strings.Contains(out, "migrate subagents") {
		t.Fatalf("doctor must name the fix for a pending migration; got:\n%s", out)
	}
	if !strings.Contains(out, "subagents") {
		t.Errorf("doctor output has no subagent-layout check:\n%s", out)
	}

	// After migrating, the check stops firing — so the report is keyed to the
	// real condition, not printed unconditionally.
	if _, err := runCLI(t, env, "migrate", "subagents", "--no-input"); err != nil {
		t.Fatal(err)
	}
	out2, _ := runCLI(t, env, "doctor")
	if strings.Contains(out2, "migrate subagents") {
		t.Errorf("doctor still reports a pending migration after it ran:\n%s", out2)
	}
}
