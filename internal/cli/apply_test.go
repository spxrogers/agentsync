package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/ui"
)

// TestApply_PartialFailureRescuesState exercises saveBestEffortState (the
// apply-error rescue, previously 0% covered): when an apply fails mid-write,
// the dest files that DID land must be recorded in state so the next apply
// doesn't reclassify them as foreign collisions and back them up. We force
// the skill write to fail (a regular file blocks the skills dir) while the
// MCP write to .claude.json (which runs first) succeeds.
func TestApply_PartialFailureRescuesState(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)

	skill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
	_ = os.MkdirAll(filepath.Dir(skill), 0o755)
	_ = os.WriteFile(skill, []byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644)

	// Block the skills destination dir with a regular file so the skill op's
	// MkdirAll fails (the MCP op to ~/.claude.json runs first and succeeds).
	_ = os.MkdirAll(filepath.Join(tmp, ".claude"), 0o755)
	if err := os.WriteFile(filepath.Join(tmp, ".claude", "skills"), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "apply"); err == nil {
		t.Fatal("expected apply to fail when the skills dir is blocked")
	}

	// The rescue must have recorded the .claude.json keys that DID land.
	state, err := os.ReadFile(filepath.Join(tmp, ".agentsync", ".state", "targets.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !strings.Contains(string(state), "/.claude.json") || !strings.Contains(string(state), "mcpServers") {
		t.Fatalf("partial-apply rescue did not record the landed .claude.json keys:\n%s", state)
	}
}

// TestApply_AnnouncesScope is the regression for silent scope selection:
// apply auto-detects project scope by walking up from cwd, but the real apply
// printed only an op count — config could land in an unexpected project tree
// invisibly. apply now announces the effective scope.
func TestApply_AnnouncesScope(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "scope: user") {
		t.Fatalf("expected user-scope announcement; got:\n%s", out)
	}

	proj := filepath.Join(tmp, "proj")
	mustRun(t, env, "init", "--scope", "project", "--project", proj)
	out2, err := runCLI(t, env, "apply", "--project", proj)
	if err != nil {
		t.Fatalf("apply --project: %v\n%s", err, out2)
	}
	if !strings.Contains(out2, "scope: project") {
		t.Fatalf("expected project-scope announcement; got:\n%s", out2)
	}
}

// TestScope_UserWithProjectConflicts is the regression for resolveScope
// silently honoring --project and ignoring an explicit --scope user (it would
// resolve project scope anyway). Contradictory flags must error.
func TestScope_UserWithProjectConflicts(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "status", "--scope", "user", "--project", proj)
	if err == nil {
		t.Fatal("expected --scope user combined with --project to be rejected")
	}
	if !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("expected a scope-conflict error, got: %v", err)
	}
}

func TestApply_DryRunEmptyHome(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "agent", "add", "claude")

	out, err := runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("apply --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "claude") {
		t.Fatalf("dry-run output missing per-agent breakdown: %s", out)
	}
	if !strings.Contains(out, "0 ops") {
		t.Fatalf("dry-run should report 0 ops on empty canonical: %s", out)
	}
}

// TestApply_FirstRunBacksUpForeignFile is the regression for the
// HIGH-severity finding: the README promised "first apply on a populated
// machine triggers foreign-collision; the original is backed up to
// ~/.agentsync/.state/backups/<ts>/" — the original code never
// implemented backups. This test runs apply against a populated $HOME
// (a hand-edited ~/.claude.json) and asserts the file is backed up
// under <home>/.state/backups/ before the apply rewrites it.
func TestApply_FirstRunBacksUpForeignFile(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Hand-edited .claude.json with a custom MCP server entry the user
	// will not want clobbered without a backup.
	claudeJSON := tmp + "/.claude.json"
	original := `{
  "mcpServers": {"github": {"command": "/usr/local/bin/my-fork", "args": ["--my-flag"]}},
  "preserveMe": "important"
}`
	if err := os.WriteFile(claudeJSON, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	// Canonical mcp/github.toml that conflicts with the existing key.
	mcpDir := tmp + "/.agentsync/mcp"
	_ = os.MkdirAll(mcpDir, 0o755)
	if err := os.WriteFile(mcpDir+"/github.toml",
		[]byte("[server]\ntype = \"stdio\"\ncommand = \"npx\"\nargs = [\"-y\",\"@m/server-github\"]\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// stderr message must mention the backup.
	if !strings.Contains(out, "backed up") {
		t.Fatalf("apply did not advertise backup; got:\n%s", out)
	}

	// Walk <home>/.state/backups looking for a copy of .claude.json with
	// the original "my-fork" command intact.
	backups, err := filepath_walkBackups(tmp + "/.agentsync/.state/backups")
	if err != nil {
		t.Fatalf("walk backups: %v", err)
	}
	if len(backups) == 0 {
		t.Fatal("no backup files written")
	}
	var found bool
	for _, b := range backups {
		data, _ := os.ReadFile(b)
		if strings.Contains(string(data), "my-fork") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no backup contains the original command; backups: %v", backups)
	}
	// And the user's foreign top-level key must still be in the live file
	// (the merge preserves disjoint keys).
	live, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(live), "preserveMe") {
		t.Fatalf("live .claude.json lost foreign key; got:\n%s", live)
	}
}

// filepath_walkBackups returns absolute paths of every regular file under
// root. Returns (nil, nil) if root does not exist so callers can assert
// "no backups written".
func filepath_walkBackups(root string) ([]string, error) {
	var out []string
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	walkErr := walkAll(root, func(p string, isDir bool) {
		if !isDir {
			out = append(out, p)
		}
	})
	return out, walkErr
}

// walkAll is a tiny recursive walker — using filepath.Walk would pull
// path/filepath into the test imports for one call site.
func walkAll(root string, visit func(string, bool)) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := root + "/" + e.Name()
		if e.IsDir() {
			visit(p, true)
			if err := walkAll(p, visit); err != nil {
				return err
			}
		} else {
			visit(p, false)
		}
	}
	return nil
}

// TestApply_DryRunPreviewsForeignCollisions is the regression test for the
// finding that `apply --dry-run` only printed op counts and silently hid
// the foreign-collision events the real apply would generate. The README
// promised dry-run was a safe preview; without collision reporting the
// user could not see which existing files were about to be backed up and
// overwritten until the real apply ran.
func TestApply_DryRunPreviewsForeignCollisions(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Populate ~/.claude.json with a server entry that conflicts.
	claudeJSON := tmp + "/.claude.json"
	original := `{"mcpServers": {"github": {"command": "/my/fork", "args": ["--x"]}}}`
	if err := os.WriteFile(claudeJSON, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	mcpDir := tmp + "/.agentsync/mcp"
	_ = os.MkdirAll(mcpDir, 0o755)
	_ = os.WriteFile(mcpDir+"/github.toml",
		[]byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\nargs=[\"-y\"]\n"),
		0o644)

	out, err := runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("apply --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Foreign collisions") {
		t.Fatalf("dry-run did not preview foreign collisions; got:\n%s", out)
	}
	if !strings.Contains(out, ".claude.json") {
		t.Fatalf("dry-run did not name the affected destination path; got:\n%s", out)
	}
	// And dry-run must not have written anything.
	bytes, _ := os.ReadFile(claudeJSON)
	if string(bytes) != original {
		t.Fatalf("dry-run mutated the live destination; got:\n%s", bytes)
	}
}

// TestApply_DryRunListsDestinations is the regression for the finding that
// dry-run reported "claude N ops" with no indication of which files would
// be touched. Users could not safely run `apply --dry-run` to learn what
// the real run would do without diff-ing every possible destination.
func TestApply_DryRunListsDestinations(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	mcpDir := tmp + "/.agentsync/mcp"
	_ = os.MkdirAll(mcpDir, 0o755)
	_ = os.WriteFile(mcpDir+"/github.toml",
		[]byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"),
		0o644)

	out, err := runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("apply --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, ".claude.json") {
		t.Fatalf("dry-run did not list destination paths; got:\n%s", out)
	}
}

// TestApply_DryRunLabelsSyncedVsWrite is the regression for the finding that
// `apply --dry-run` labeled every op "write" even when the destination already
// held our exact bytes — so a dry-run on an in-sync tree looked like pending
// work. After a real apply everything is in sync, so the dry-run must label the
// destinations "synced" (0 to write); editing one source then surfaces it as a
// "write" while the untouched one stays "synced".
func TestApply_DryRunLabelsSyncedVsWrite(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	mkSkill := func(name, desc string) {
		t.Helper()
		p := filepath.Join(tmp, ".agentsync", "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: " + desc + "\n---\nbody\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkSkill("alpha", "first")
	mkSkill("bravo", "second")

	// Sync both skills to disk.
	mustRun(t, env, "apply")

	// A dry-run now finds both rendered skills already in sync.
	out, err := runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("apply --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, ui.GlyphOK+" synced") {
		t.Fatalf("dry-run did not label already-synced destinations; got:\n%s", out)
	}
	if !strings.Contains(out, "0 to write") {
		t.Fatalf("dry-run summary did not report a clean no-op; got:\n%s", out)
	}
	if strings.Contains(out, ui.GlyphArrow+" write") {
		t.Fatalf("dry-run labeled an in-sync destination as a write; got:\n%s", out)
	}

	// Edit one source: that skill must now show a pending write while the
	// untouched one stays synced.
	mkSkill("alpha", "edited")
	out, err = runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("apply --dry-run (after edit): %v\n%s", err, out)
	}
	if !strings.Contains(out, ui.GlyphArrow+" write") {
		t.Fatalf("dry-run did not surface the pending write; got:\n%s", out)
	}
	if !strings.Contains(out, "1 to write") {
		t.Fatalf("dry-run summary miscounted pending writes; got:\n%s", out)
	}
	if !strings.Contains(out, ui.GlyphOK+" synced") {
		t.Fatalf("dry-run lost the still-synced destination; got:\n%s", out)
	}
}

func TestApply_NoAgentsEnabled_WarnsAndExitsZero(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	// Deliberately do NOT call `agent add`. apply must hint at the fix
	// instead of silently printing "applied: 0 ops".
	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply with no agents should succeed; got: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no agents") || !strings.Contains(out, "agent add") {
		t.Fatalf("apply with no agents did not hint at remediation; got: %s", out)
	}
}

func TestApply_NoFlag_WritesDestinations_M1(t *testing.T) {
	// M1+: apply (no flag) should succeed (real adapters are wired).
	// With an empty canonical there are 0 ops, so no files are written, but
	// the command must not error.
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "agent", "add", "claude")

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply in M1 should succeed; got err: %v\n%s", err, out)
	}
}

// TestApply_SkillOrphanCleanup verifies apply converges the destination when a
// skill — or a single bundled file within one — is removed from the canonical
// source: the leftover dest files (and the empty dirs they leave) are reclaimed,
// while a destination the user hand-edited is backed up before removal.
func TestApply_SkillOrphanCleanup(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	srcSkill := filepath.Join(tmp, ".agentsync", "skills", "deploy")
	writeSkill := func() {
		_ = os.MkdirAll(filepath.Join(srcSkill, "scripts"), 0o755)
		_ = os.MkdirAll(filepath.Join(srcSkill, "references"), 0o755)
		if err := os.WriteFile(filepath.Join(srcSkill, "SKILL.md"), []byte("---\nname: deploy\ndescription: d\n---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(srcSkill, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(srcSkill, "references", "REF.md"), []byte("# ref\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	destSkill := filepath.Join(tmp, ".claude", "skills", "deploy")
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	// 1. Apply the full skill directory.
	writeSkill()
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 1: %v\n%s", err, out)
	}
	for _, rel := range []string{"SKILL.md", "scripts/run.sh", "references/REF.md"} {
		if !exists(filepath.Join(destSkill, filepath.FromSlash(rel))) {
			t.Fatalf("apply 1 did not write %s", rel)
		}
	}

	// 2. Remove a single bundled file from source; apply must reclaim it and
	//    prune the now-empty references/ dir, leaving the rest intact.
	if err := os.RemoveAll(filepath.Join(srcSkill, "references")); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 2: %v\n%s", err, out)
	}
	if exists(filepath.Join(destSkill, "references", "REF.md")) {
		t.Fatal("orphaned bundled file references/REF.md was not deleted")
	}
	if exists(filepath.Join(destSkill, "references")) {
		t.Fatal("empty references/ dir was not pruned")
	}
	if !exists(filepath.Join(destSkill, "SKILL.md")) || !exists(filepath.Join(destSkill, "scripts", "run.sh")) {
		t.Fatal("apply 2 wrongly removed surviving skill files")
	}

	// 3. Remove the whole skill from source; apply must reclaim the entire
	//    destination directory.
	if err := os.RemoveAll(srcSkill); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 3: %v\n%s", err, out)
	}
	if exists(destSkill) {
		t.Fatalf("orphaned skill directory %s was not removed", destSkill)
	}
	// The skills root itself must survive (it may hold other skills).
	if !exists(filepath.Join(tmp, ".claude", "skills")) {
		t.Fatal("skills root was wrongly pruned")
	}

	// 4. Drift: re-apply, hand-edit the dest SKILL.md, then remove the source
	//    skill. Apply must back the edit up before deleting it.
	writeSkill()
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 4: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(destSkill, "SKILL.md"), []byte("HAND EDITED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(srcSkill); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 5: %v\n%s", err, out)
	}
	if exists(destSkill) {
		t.Fatal("drifted orphan skill was not removed")
	}
	if !backupContains(t, filepath.Join(tmp, ".agentsync", ".state", "backups"), "HAND EDITED\n") {
		t.Fatal("hand-edited skill file was deleted without a backup")
	}
}

// backupContains reports whether any file under root has the given content.
func backupContains(t *testing.T, root, want string) bool {
	t.Helper()
	found := false
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr == nil && string(data) == want {
			found = true
		}
		return nil
	})
	return found
}
