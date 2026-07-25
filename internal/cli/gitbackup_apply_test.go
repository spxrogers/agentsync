package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	agit "github.com/spxrogers/agentsync/internal/git"
)

// TestApply_GitBackupCheckpoint is the end-to-end apply-tail test: with git backup
// enabled, the first apply that writes under ~/.claude initializes a local repo and
// records a PRE-APPLY BASELINE plus the apply checkpoint (≥2 commits, the apply
// checkpoint's parent being the baseline — issue #143); a re-apply with no source
// change records none.
func TestApply_GitBackupCheckpoint(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	// No TTY in tests, so the prompt is unavailable — enable git backup via config.
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n[destination_directory_git_backup]\nmode = \"on\"\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// A skill renders to ~/.claude/skills/demo/SKILL.md — inside the versioned dir
	// (an MCP-only config would only touch ~/.claude.json at $HOME, which is out of
	// scope by design).
	skill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	claude := filepath.Join(tmp, ".claude")
	st, err := agit.Detect(claude)
	if err != nil {
		t.Fatal(err)
	}
	if st != agit.StateAgentsyncOwned {
		t.Fatalf("~/.claude state = %v, want agentsync-owned", st)
	}
	repo, err := agit.Open(claude)
	if err != nil {
		t.Fatal(err)
	}
	cps, err := repo.Log(0)
	if err != nil {
		t.Fatal(err)
	}
	// ≥2 commits: a pre-apply baseline plus the apply checkpoint. The old "exactly 1
	// checkpoint after first apply" premise is exactly the recoverability gap #143
	// closed — before the baseline, the first apply was unrevertible.
	if len(cps) < 2 {
		t.Fatalf("want >=2 commits after first apply (baseline + apply), got %d", len(cps))
	}
	// The apply checkpoint (HEAD) is parented on the pre-apply baseline.
	gr, err := gogit.PlainOpen(claude)
	if err != nil {
		t.Fatal(err)
	}
	head, err := gr.Head()
	if err != nil {
		t.Fatal(err)
	}
	hc, err := gr.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if hc.NumParents() != 1 {
		t.Fatalf("apply checkpoint should have exactly one parent (the baseline), got %d", hc.NumParents())
	}
	baseline := cps[len(cps)-1] // oldest commit
	if hc.ParentHashes[0].String() != baseline.Hash {
		t.Fatalf("apply checkpoint parent = %s, want the baseline %s", hc.ParentHashes[0].String(), baseline.Hash)
	}
	if !strings.Contains(baseline.Subject, "pre-apply baseline") {
		t.Fatalf("oldest commit should be the pre-apply baseline, got subject %q", baseline.Subject)
	}

	// Re-apply with no source change → no new checkpoint (apply is a no-op): neither
	// the baseline pass (worktree clean) nor the checkpoint pass records anything.
	before := len(cps)
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("re-apply: %v\n%s", err, out)
	}
	cps2, _ := repo.Log(0)
	if len(cps2) != before {
		t.Fatalf("re-apply recorded a checkpoint despite no change: %d -> %d commits", before, len(cps2))
	}
}

// trackedInCommit reports whether rel is present in the tree of commit `hash`.
func trackedInCommit(t *testing.T, dir, hash, rel string) bool {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.CommitObject(plumbing.NewHash(hash))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := c.Tree()
	if err != nil {
		t.Fatal(err)
	}
	_, err = tree.File(rel)
	return err == nil
}

// TestApply_GitBackupBaselineRevertsFirstApply is the headline #143 test, anchored to
// the on-disk artifact: a populated destination dir's pre-apply bytes are written to
// disk first, then a first apply + revert must restore those exact bytes (not a parsed
// model), and revert must NOT print "only one checkpoint — skipping".
func TestApply_GitBackupBaselineRevertsFirstApply(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "demo", "applied-body")

	// Seed the destination dir with genuine pre-apply content ON DISK, before the
	// first apply ever runs. existing.txt is a file agentsync never writes.
	claude := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	const preApply = "PRE-APPLY-BYTES\nkeep me exactly\n"
	existing := filepath.Join(claude, "existing.txt")
	if err := os.WriteFile(existing, []byte(preApply), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// Precondition: the apply wrote a managed skill under the dir, and the repo has a
	// baseline + checkpoint.
	skillDest := filepath.Join(claude, "skills", "demo", "SKILL.md")
	if _, err := os.Stat(skillDest); err != nil {
		t.Fatalf("apply should have written the skill: %v", err)
	}
	repo, err := agit.Open(claude)
	if err != nil {
		t.Fatal(err)
	}
	if cps, _ := repo.Log(0); len(cps) < 2 {
		t.Fatalf("first apply should record a baseline + checkpoint (>=2 commits), got %d", len(cps))
	}

	out, err := runCLI(t, env, "revert", "claude")
	if err != nil {
		t.Fatalf("revert: %v\n%s", err, out)
	}
	if strings.Contains(out, "only one checkpoint") {
		t.Fatalf("revert bailed with the single-checkpoint message despite a baseline:\n%s", out)
	}
	// The pre-apply bytes are restored byte-for-byte...
	if b, _ := os.ReadFile(existing); string(b) != preApply {
		t.Fatalf("revert did not restore pre-apply existing.txt byte-for-byte:\n got %q\nwant %q", string(b), preApply)
	}
	// ...and the applied skill is rolled back (the pre-apply state had none).
	if _, err := os.Stat(skillDest); !os.IsNotExist(err) {
		t.Fatalf("revert should have removed the applied skill (pre-apply state had none); stat err=%v", err)
	}
}

// TestApply_GitBackupBaselinePreexistingContentOutOfContract pins issue #143's
// pre-existing-content decision (Problem B). The pre-apply baseline stages only the
// NOTICE plus the managed paths' pre-apply content — NOT the whole dir. Pre-existing,
// non-managed siblings (a stray user file, a leftover skill, and — critically — an
// agent credentials file) are deliberately OUT of the recoverability contract: they
// are NOT swept into the local-only git history (which would durably persist a live
// credential far beyond the resolved-config secret model), yet they survive a revert
// untouched because #128 makes Restore untracked-safe.
func TestApply_GitBackupBaselinePreexistingContentOutOfContract(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "demo", "applied")

	claude := filepath.Join(tmp, ".claude")
	// Pre-existing, non-managed content on disk before the first apply: a stray note,
	// a leftover skill agentsync does NOT write this run, and a file mimicking an
	// agent's live credentials.
	seed := map[string]string{
		"notes.txt":                "stray user note\n",
		"skills/leftover/SKILL.md": "---\nname: leftover\n---\nold\n",
		".credentials.json":        "{\"token\":\"live-oauth-secret\"}\n",
	}
	for rel, body := range seed {
		abs := filepath.Join(claude, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	repo, err := agit.Open(claude)
	if err != nil {
		t.Fatal(err)
	}
	cps, err := repo.Log(0)
	if err != nil || len(cps) < 2 {
		t.Fatalf("want >=2 commits, got %d (err %v)", len(cps), err)
	}
	baseline := cps[len(cps)-1].Hash // oldest = the pre-apply baseline
	for rel := range seed {
		if trackedInCommit(t, claude, baseline, rel) {
			t.Errorf("non-managed %q must NOT be versioned in the baseline (secret-history surface)", rel)
		}
		// ...but it must survive on disk untouched (untracked, preserved by #128).
		if _, err := os.Stat(filepath.Join(claude, filepath.FromSlash(rel))); err != nil {
			t.Errorf("pre-existing non-managed %q should be preserved on disk after apply, got %v", rel, err)
		}
	}
	// Guard against a vacuous pass: trackedInCommit must actually return TRUE for a
	// path the baseline DOES version — the agentsync NOTICE it writes at init.
	if !trackedInCommit(t, claude, baseline, agit.NoticeFile) {
		t.Errorf("the agentsync NOTICE (%s) should be tracked in the baseline commit", agit.NoticeFile)
	}
}

// TestApply_GitBackupBaselineRestoresPreexistingManagedFile proves the baseline's
// os.Stat-succeeds branch (gitbackup.go): a MANAGED destination file that already exists
// on disk before the first (git-backed) apply has its pre-apply content captured in the
// baseline, so a revert restores those exact bytes. This is the data-loss-on-rollback
// guard the narrowed baseline (issue #143) must still honor — the sibling
// ...RevertsFirstApply test's surviving file is non-managed, so it only proves #128
// untracked-preservation, not managed capture.
func TestApply_GitBackupBaselineRestoresPreexistingManagedFile(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "demo", "new-applied-body")

	// A MANAGED dest file already exists on disk before the first git-backed apply
	// (e.g. a prior `apply --no-git-backup`, or a hand-placed file at a path agentsync
	// manages). Its pre-apply bytes differ from what this apply will render.
	claude := filepath.Join(tmp, ".claude")
	skillDest := filepath.Join(claude, "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillDest), 0o755); err != nil {
		t.Fatal(err)
	}
	const preApplyManaged = "OLD-MANAGED-PRE-APPLY-BYTES\nkeep me exactly\n"
	if err := os.WriteFile(skillDest, []byte(preApplyManaged), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// The apply overwrote the managed dest with freshly-rendered content.
	if b, _ := os.ReadFile(skillDest); string(b) == preApplyManaged {
		t.Fatalf("apply should have overwritten the managed dest; it still holds the pre-apply bytes")
	}
	repo, err := agit.Open(claude)
	if err != nil {
		t.Fatal(err)
	}
	if cps, _ := repo.Log(0); len(cps) < 2 {
		t.Fatalf("first apply should record a baseline + checkpoint (>=2 commits), got %d", len(cps))
	}

	// Revert restores the MANAGED dest to its pre-apply bytes captured by the baseline.
	if out, err := runCLI(t, env, "revert", "claude"); err != nil {
		t.Fatalf("revert: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(skillDest); string(b) != preApplyManaged {
		t.Fatalf("revert did not restore the managed dest's pre-apply bytes:\n got %q\nwant %q", string(b), preApplyManaged)
	}
}

// TestApply_GitBackupBaselineFailureDoesNotAbortApply drives a case where the pre-apply
// baseline cannot be taken (a foreign repo nested below the root fires the nested-repo
// guard) and asserts the apply still succeeds, the destination is still rendered, and
// the user is warned the first apply is not revertible.
func TestApply_GitBackupBaselineFailureDoesNotAbortApply(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "demo", "applied")

	claude := filepath.Join(tmp, ".claude")
	// A foreign git repo already lives BELOW ~/.claude, so the nested-repo guard must
	// refuse to init a wrapping repo — the baseline can't be taken there.
	inner := filepath.Join(claude, "skills", "vendor")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := gogit.PlainInit(inner, false); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply must still succeed despite the baseline skip: %v\n%s", err, out)
	}
	// The destination is still rendered.
	if _, err := os.Stat(filepath.Join(claude, "skills", "demo", "SKILL.md")); err != nil {
		t.Fatalf("apply should still render the skill: %v", err)
	}
	// No wrapping repo was created at ~/.claude (the nested guard fired) ...
	if _, err := os.Stat(filepath.Join(claude, ".git")); !os.IsNotExist(err) {
		t.Fatalf("~/.claude/.git must not exist (nested guard should skip); err=%v", err)
	}
	// ... and the user is warned the first apply is not revertible.
	if !strings.Contains(out, "not be revertible") {
		t.Errorf("expected a 'first apply not revertible' warning, got:\n%s", out)
	}
}

// TestModeOnNewDirEmitsCleartextNotice pins issue #126's mode="on" caution: an apply
// that auto-inits NEW dirs prints the "local-only / may contain cleartext" notice once
// per run (even across several new dirs); a later apply into the now-owned dirs does not.
func TestModeOnNewDirEmitsCleartextNotice(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "codex") // adds ~/.agents/skills as a second new root
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "demo", "d") // renders under ~/.claude AND ~/.agents/skills

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// Exactly once, even though more than one new dir was inited this run.
	if n := strings.Count(out, "may contain secrets in cleartext"); n != 1 {
		t.Fatalf("mode=on cleartext notice should appear exactly once across new dirs, got %d:\n%s", n, out)
	}
	// …but EVERY inited dir must announce itself: only the caution clause is
	// once-per-run. Before the round-3 fix the whole notice was gated, so the
	// second dir was inited silently.
	if n := strings.Count(out, "git backup of"); n < 2 {
		t.Fatalf("every mode=on auto-inited dir should announce itself (want >=2 announcements), got %d:\n%s", n, out)
	}

	// A second apply (no source change) only commits into now-owned dirs — no notice.
	out2, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("re-apply: %v\n%s", err, out2)
	}
	if strings.Contains(out2, "may contain secrets in cleartext") {
		t.Errorf("second apply into owned dirs must not re-emit the cleartext notice:\n%s", out2)
	}
}

// TestApply_BaselineSnapshotPrintsCleartextCaution pins the apply-time side of
// issue #126: when the pre-apply baseline actually commits something (here, a
// hand-typed edit to a tracked dest file since the last apply — exactly the class
// of content that may hold a freshly-typed secret), apply prints the same
// cleartext caution the revert snapshot prints, once per run.
func TestApply_BaselineSnapshotPrintsCleartextCaution(t *testing.T) {
	_, env, destSkill := setupGitBackedClaude(t)

	// Hand-edit a tracked dest file; the next apply's baseline snapshots it into
	// the local-only history before overwriting it.
	if err := os.WriteFile(destSkill, []byte("HAND-TYPED ghp_fresh_secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pre-apply baseline") {
		t.Fatalf("expected a baseline commit for the hand-edit, got:\n%s", out)
	}
	if n := strings.Count(out, "may contain secrets in cleartext"); n != 1 {
		t.Fatalf("baseline cleartext caution should appear exactly once, got %d:\n%s", n, out)
	}

	// A clean re-apply commits no baseline — and so must print no caution.
	out2, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("re-apply: %v\n%s", err, out2)
	}
	if strings.Contains(out2, "may contain secrets in cleartext") {
		t.Errorf("a no-op apply must not re-emit the cleartext caution:\n%s", out2)
	}
}

// TestApply_NoBaselineCommitInUntouchedRoot pins the baseline pass's scope: an
// agentsync-owned root that this apply plans NO write or delete under must not
// grow a "pre-apply baseline" commit, even when it holds uncommitted tracked
// drift. (resolveBackupRepo opens owned roots unconditionally for the checkpoint
// pass's delete-only case; the baseline pass must not snapshot through that into
// dirs the apply never touches — the drift stays on disk, exactly as a
// --no-git-backup run would leave it.)
func TestApply_NoBaselineCommitInUntouchedRoot(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "demo", "d")
	if out, err := runCLI(t, env, "apply"); err != nil { // inits ~/.claude: baseline + checkpoint
		t.Fatalf("apply 1: %v\n%s", err, out)
	}
	// Drop the skill from source; the delete-only apply removes it (planned delete).
	if err := os.RemoveAll(filepath.Join(tmp, ".agentsync", "skills", "demo")); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 2: %v\n%s", err, out)
	}

	// ~/.claude now receives NO planned paths at all. Dirty a TRACKED file (the
	// notice) so a snapshot WOULD have something to commit if it ran.
	claude := filepath.Join(tmp, ".claude")
	const drift = "hand-edited notice — uncommitted drift\n"
	if err := os.WriteFile(filepath.Join(claude, agit.NoticeFile), []byte(drift), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, err := agit.Open(claude)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := repo.Log(0)

	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 3: %v\n%s", err, out)
	}
	after, _ := repo.Log(0)
	if len(after) != len(before) {
		t.Fatalf("an apply with no planned path under ~/.claude must record no baseline there: %d -> %d commits", len(before), len(after))
	}
	// The drift is untouched on disk and still uncommitted.
	if b, _ := os.ReadFile(filepath.Join(claude, agit.NoticeFile)); string(b) != drift {
		t.Fatalf("untouched root's drift was disturbed: %q", b)
	}
	if clean, _ := repo.IsClean(); clean {
		t.Fatal("the tracked drift must remain uncommitted (no snapshot ran)")
	}
}

// committedBlob returns the contents of rel in the repo-at-dir's HEAD commit tree.
func committedBlob(t *testing.T, dir, rel string) string {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := c.Tree()
	if err != nil {
		t.Fatal(err)
	}
	f, err := tree.File(rel)
	if err != nil {
		t.Fatalf("blob %q not in HEAD tree: %v", rel, err)
	}
	s, err := f.Contents()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestResolvedSecretLandsInLocalHistory anchors the blessed local-only cleartext
// exception (issue #126) to the on-disk artifact: a hook command referencing an
// ${env:…} secret resolves to CLEARTEXT in the rendered settings.json, which is
// committed into the destination dir's local-only git history. The rationale ("this
// history really may contain secrets") is only true if the cleartext is actually there.
func TestResolvedSecretLandsInLocalHistory(t *testing.T) {
	const secretVal = "s3cr3t-in-history-9x"
	tmp := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmp,
		"AGENTSYNC_HIST_SECRET": secretVal,
	}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	enableGitBackupOn(t, tmp)

	// A hook whose command carries an ${env:…} reference — resolved to cleartext into
	// ~/.claude/settings.json at apply time.
	hook := filepath.Join(tmp, ".agentsync", "hooks", "PreToolUse.toml")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("[[hook]]\nmatcher = \"Write\"\ntype = \"command\"\ncommand = \"echo ${env:AGENTSYNC_HIST_SECRET}\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	claude := filepath.Join(tmp, ".claude")
	// The resolved cleartext is on disk...
	if b, _ := os.ReadFile(filepath.Join(claude, "settings.json")); !strings.Contains(string(b), secretVal) {
		t.Fatalf("resolved secret should be cleartext in settings.json on disk:\n%s", b)
	}
	// ...and in the committed history blob — the exception is real.
	if blob := committedBlob(t, claude, "settings.json"); !strings.Contains(blob, secretVal) {
		t.Fatalf("resolved secret cleartext should be committed to the local-only history:\n%s", blob)
	}
}

// writeSkillSource writes a skill SKILL.md into the canonical source tree.
func writeSkillSource(t *testing.T, tmp, name, body string) {
	t.Helper()
	p := filepath.Join(tmp, ".agentsync", "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\nname: "+name+"\ndescription: d\n---\n"+body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func enableGitBackupOn(t *testing.T, tmp string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n[destination_directory_git_backup]\nmode = \"on\"\n")
	_ = f.Close()
}

// TestApply_GitBackupRecordsDeletion proves a managed-file DELETION is committed to
// the checkpoint (not left as silent worktree drift). If the deletion weren't
// committed, the repo would be dirty (worktree missing a file HEAD still has).
func TestApply_GitBackupRecordsDeletion(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "keep", "k")
	writeSkillSource(t, tmp, "temp", "t")
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 1: %v\n%s", err, out)
	}

	tempDest := filepath.Join(tmp, ".claude", "skills", "temp", "SKILL.md")
	if _, err := os.Stat(tempDest); err != nil {
		t.Fatalf("precondition: temp skill should exist after apply 1: %v", err)
	}

	// Remove the temp skill from source and re-apply → its dest file is deleted.
	if err := os.RemoveAll(filepath.Join(tmp, ".agentsync", "skills", "temp")); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 2: %v\n%s", err, out)
	}
	if _, err := os.Stat(tempDest); !os.IsNotExist(err) {
		t.Fatalf("temp dest skill should be deleted after apply 2, stat err = %v", err)
	}

	repo, err := agit.Open(filepath.Join(tmp, ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	// The deletion was committed iff the worktree matches HEAD (clean).
	clean, err := repo.IsClean()
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Fatal("deletion was not committed to the checkpoint (repo is dirty vs HEAD)")
	}
	cps, _ := repo.Log(0)
	if len(cps) < 2 {
		t.Fatalf("want >=2 checkpoints (add, then delete), got %d", len(cps))
	}
}

// TestApply_GitBackupVersionsSharedSkillsDir proves the shared cross-vendor
// ~/.agents/skills dir is git-versioned (Codex declares it as a version root).
func TestApply_GitBackupVersionsSharedSkillsDir(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "codex")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "demo", "d")
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	shared := filepath.Join(tmp, ".agents", "skills")
	if _, err := os.Stat(filepath.Join(shared, "demo", "SKILL.md")); err != nil {
		t.Fatalf("codex should render the skill to %s: %v", shared, err)
	}
	st, err := agit.Detect(shared)
	if err != nil {
		t.Fatal(err)
	}
	if st != agit.StateAgentsyncOwned {
		t.Fatalf("shared %s state = %v, want agentsync-versioned", shared, st)
	}
}

// TestApply_GitBackupGuardsAgainstNestedRepo is the regression for the cross-run
// nesting BLOCKER: an earlier opencode-only apply versions ~/.claude/skills; later
// enabling claude must NOT init a repo at ~/.claude that would wrap the child repo.
func TestApply_GitBackupGuardsAgainstNestedRepo(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "opencode")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "demo", "d")
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply (opencode): %v\n%s", err, out)
	}
	claudeSkills := filepath.Join(tmp, ".claude", "skills")
	if st, _ := agit.Detect(claudeSkills); st != agit.StateAgentsyncOwned {
		t.Fatalf("opencode should have versioned %s, state=%v", claudeSkills, st)
	}

	// Now add claude (whose root ~/.claude wraps the existing ~/.claude/skills repo).
	mustRun(t, env, "agent", "add", "claude")
	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply (claude): %v\n%s", err, out)
	}
	if !strings.Contains(out, "nested git repository") {
		t.Errorf("expected a nested-repo warning, got:\n%s", out)
	}
	// The guard fired: no repo was created at ~/.claude (it would wrap the child).
	if _, err := os.Stat(filepath.Join(tmp, ".claude", ".git")); !os.IsNotExist(err) {
		t.Fatalf("~/.claude/.git should not exist (guard must skip the wrapping init); err=%v", err)
	}
	// The child repo is intact — no corruption.
	if st, _ := agit.Detect(claudeSkills); st != agit.StateAgentsyncOwned {
		t.Fatalf("child repo %s damaged, state=%v", claudeSkills, st)
	}
}

// headCount returns the number of commits reachable from HEAD, or 0 if HEAD is
// unborn (a freshly `git init`'d repo with no commits).
func headCount(t *testing.T, dir string) int {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	ref, err := repo.Head()
	if err != nil {
		return 0 // unborn HEAD: no commits yet
	}
	iter, err := repo.Log(&gogit.LogOptions{From: ref.Hash()})
	if err != nil {
		t.Fatalf("log %s: %v", dir, err)
	}
	n := 0
	_ = iter.ForEach(func(*object.Commit) error { n++; return nil })
	return n
}

// headHash returns the HEAD commit hash, or "" for an unborn HEAD.
func headHash(t *testing.T, dir string) string {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	ref, err := repo.Head()
	if err != nil {
		return ""
	}
	return ref.Hash().String()
}

// seedUserCommit makes a real commit in a foreign repo at dir (a non-agentsync
// identity, fixed time for determinism) and returns its hash. Used so the
// "agentsync must not touch the user's history" assertions bite against a concrete
// HEAD rather than an unborn one.
func seedUserCommit(t *testing.T, dir, rel, content string) string {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree %s: %v", dir, err)
	}
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(rel); err != nil {
		t.Fatalf("add %s: %v", rel, err)
	}
	sig := &object.Signature{Name: "real-user", Email: "user@example.com", When: time.Unix(1_700_000_000, 0).UTC()}
	h, err := wt.Commit("user's own commit", &gogit.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatalf("commit %s: %v", rel, err)
	}
	return h.String()
}

// staging returns the index (staging) status code of rel within the repo at dir.
// A file agentsync merely rendered (never `git add`ed) reads as Untracked; if
// agentsync had staged it into the user's index it would read as Added.
func staging(t *testing.T, dir, rel string) gogit.StatusCode {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree %s: %v", dir, err)
	}
	st, err := wt.Status()
	if err != nil {
		t.Fatalf("status %s: %v", dir, err)
	}
	return st.File(filepath.ToSlash(rel)).Staging
}

// assertUserRepoUntouched is the shared, behavior-level contract for decision #5
// ("Respect existing source control"): after an apply that rendered renderedRel
// into a directory the user already git-controls (worktree root userRepo),
// agentsync must have left that repo PRISTINE — HEAD unmoved, no agentsync marker,
// no local-history notice file, and the rendered file present-but-UNTRACKED (never
// staged or committed into the user's index/history).
func assertUserRepoUntouched(t *testing.T, userRepo, renderedRel, wantHead string) {
	t.Helper()
	// HEAD did not move: agentsync committed nothing into the user's history.
	if got := headHash(t, userRepo); got != wantHead {
		t.Fatalf("agentsync moved the user's HEAD: %q → %q (must not commit into a user repo)", wantHead, got)
	}
	// No agentsync marker: the repo was never claimed/re-inited.
	if owned, _ := agit.OwnsExactly(userRepo); owned {
		t.Fatalf("agentsync stamped its marker onto the user's repo at %s", userRepo)
	}
	// No local-history notice: that file is written ONLY by agit.Init, so its
	// presence would prove an init happened.
	if _, err := os.Stat(filepath.Join(userRepo, agit.NoticeFile)); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist in a user repo (proves agentsync inited); stat err=%v", agit.NoticeFile, err)
	}
	// The rendered file exists but is UNTRACKED — agentsync rendered it without
	// staging it into the user's index. Added/any-staged here would mean agentsync
	// ran `git add` against the user's repo.
	if code := staging(t, userRepo, renderedRel); code != gogit.Untracked {
		t.Fatalf("rendered %s has staging code %q in the user's index, want Untracked (agentsync must not stage into a user repo)", renderedRel, string(code))
	}
}

// assertSkippedNotInited asserts the apply output shows the clean decision-#5 skip
// (a hint naming the dir + "source control") and crucially does NOT show an
// init-failure — distinguishing "the foreign guard cleanly skipped" from "agentsync
// tried to init and bounced off go-git's already-exists error". Without this, a
// regression that deletes the guard could still pass on an exact-dir repo because
// PlainInit refuses a second init anyway.
func assertSkippedNotInited(t *testing.T, out, dir string) {
	t.Helper()
	if !strings.Contains(out, "source control") || !strings.Contains(out, dir) {
		t.Errorf("expected a decision-#5 skip hint naming %s and 'source control', got:\n%s", dir, out)
	}
	if strings.Contains(out, "already exists") {
		t.Errorf("apply attempted to init over the user's repo (saw 'already exists') instead of cleanly skipping:\n%s", out)
	}
}

// TestApply_GitBackupSkipsForeignRepoAtDir is the regression for the user's vector:
// when ~/.claude is ALREADY under the user's own source control (a foreign `.git`
// exactly at the dir, with real history), apply must render the skill yet leave the
// user's repo byte-for-byte pristine and cleanly skip — never init, stage, or commit.
func TestApply_GitBackupSkipsForeignRepoAtDir(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "demo", "d")

	// The user already source-controls ~/.claude: a foreign (unmarked) repo at the
	// dir, with a real commit so HEAD invariance is a meaningful assertion.
	claude := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := gogit.PlainInit(claude, false); err != nil {
		t.Fatal(err)
	}
	wantHead := seedUserCommit(t, claude, "README.md", "the user's own file")

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	// apply still rendered the skill — versioning being skipped must not block apply.
	rendered := filepath.Join("skills", "demo", "SKILL.md")
	if _, err := os.Stat(filepath.Join(claude, rendered)); err != nil {
		t.Fatalf("apply should still render the skill into the foreign dir: %v", err)
	}
	// The dir is still the USER's repo, untouched in every observable way.
	if st, _ := agit.Detect(claude); st != agit.StateForeign {
		t.Fatalf("~/.claude state = %v, want StateForeign (agentsync must not claim a user repo)", st)
	}
	if n := headCount(t, claude); n != 1 {
		t.Fatalf("user history changed: HEAD has %d commits, want exactly the 1 the user made", n)
	}
	assertUserRepoUntouched(t, claude, rendered, wantHead)
	assertSkippedNotInited(t, out, claude)
}

// TestApply_GitBackupSkipsDirNestedInForeignRepo covers the dotfiles case: the user
// keeps their whole home dir under git, so ~/.claude is nested inside a parent
// foreign repo. DetectDotGit must find the parent and report StateForeign, so apply
// renders into ~/.claude yet neither inits a nested repo there nor stages/commits
// into the parent.
func TestApply_GitBackupSkipsDirNestedInForeignRepo(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	enableGitBackupOn(t, tmp)
	writeSkillSource(t, tmp, "demo", "d")

	// Model a dotfiles repo at $HOME: the target root itself is a foreign git repo
	// with real history, so every dest dir below it (~/.claude, …) is already
	// user-source-controlled.
	if _, err := gogit.PlainInit(tmp, false); err != nil {
		t.Fatal(err)
	}
	wantHead := seedUserCommit(t, tmp, "dotfiles-readme.md", "my dotfiles")

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	claude := filepath.Join(tmp, ".claude")
	// Rendered file path relative to the PARENT repo's worktree root (tmp).
	rendered := filepath.Join(".claude", "skills", "demo", "SKILL.md")
	if _, err := os.Stat(filepath.Join(tmp, rendered)); err != nil {
		t.Fatalf("apply should still render the skill: %v", err)
	}
	// No nested repo was created at ~/.claude — this is the assertion that bites if
	// the foreign guard regresses (go-git would happily init a child repo here).
	if _, err := os.Stat(filepath.Join(claude, ".git")); !os.IsNotExist(err) {
		t.Fatalf("~/.claude/.git should not exist (must not nest in the user's repo); err=%v", err)
	}
	if st, _ := agit.Detect(claude); st != agit.StateForeign {
		t.Fatalf("~/.claude state = %v, want StateForeign via the parent repo", st)
	}
	if n := headCount(t, tmp); n != 1 {
		t.Fatalf("parent history changed: HEAD has %d commits, want exactly the 1 the user made", n)
	}
	assertUserRepoUntouched(t, tmp, rendered, wantHead)
	assertSkippedNotInited(t, out, claude)
}

// TestDoctor_RejectsUnknownGitBackupModeAtLoad checks that a typo'd git-backup
// mode is now caught at source.Load (via validateMode), so doctor surfaces it as
// an invalid schema and returns an error rather than reaching the git-backup
// section's defensive warn arm. (Previously the value slipped past strict
// key-decoding and only the git-backup section warned about it — see issue #137.)
func TestDoctor_RejectsUnknownGitBackupModeAtLoad(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	f, _ := os.OpenFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"), os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("\n[destination_directory_git_backup]\nmode = \"bogus\"\n")
	_ = f.Close()
	out, err := runCLI(t, env, "doctor")
	if err == nil {
		t.Fatalf("doctor should fail on the invalid mode; got no error\n%s", out)
	}
	// The schema check reports the load error, which names the offending value
	// and the valid set.
	if !strings.Contains(out, "invalid") || !strings.Contains(out, "bogus") {
		t.Fatalf("doctor should report the invalid mode at schema load; got:\n%s", out)
	}
}

// TestDoctor_ShowsVersionedRoot checks doctor reports a versioned dir's status.
func TestDoctor_ShowsVersionedRoot(t *testing.T) {
	_, env, _ := setupGitBackedClaude(t)
	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "agentsync-versioned") {
		t.Fatalf("doctor should report ~/.claude as agentsync-versioned; got:\n%s", out)
	}
}

// TestApply_NoGitBackupFlag verifies --no-git-backup skips versioning even when
// mode is "on".
func TestApply_NoGitBackupFlag(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	f, _ := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("\n[destination_directory_git_backup]\nmode = \"on\"\n")
	_ = f.Close()

	skill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
	_ = os.MkdirAll(filepath.Dir(skill), 0o755)
	_ = os.WriteFile(skill, []byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644)

	if out, err := runCLI(t, env, "apply", "--no-git-backup"); err != nil {
		t.Fatalf("apply --no-git-backup: %v\n%s", err, out)
	}
	st, _ := agit.Detect(filepath.Join(tmp, ".claude"))
	if st == agit.StateAgentsyncOwned {
		t.Fatal("--no-git-backup must not create a repo")
	}
}

// TestApply_GitBackupBaselineCoversOrphanDeletes closes the planned-deletes gap
// in issue #143's baseline: an apply whose only change is REMOVING a managed
// file (a skill dropped from source) deletes an in-sync orphan without a writer
// backup, so unless the pre-apply baseline stages that file's bytes, the first
// git-backed apply's deletion is unrecoverable — no baseline, no checkpoint
// (never tracked), no backup dir. Scenario: the file was written by an earlier
// apply with git backup OFF (state entries exist, no repo), then backup is
// enabled on the delete-only apply.
func TestApply_GitBackupBaselineCoversOrphanDeletes(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	writeSkillSource(t, tmp, "doomed", "precious-body")

	// First apply with backup OFF: the skill lands on disk and in state, but in
	// no git history.
	if out, err := runCLI(t, env, "apply", "--no-git-backup"); err != nil {
		t.Fatalf("apply --no-git-backup: %v\n%s", err, out)
	}
	claude := filepath.Join(tmp, ".claude")
	skillDest := filepath.Join(claude, "skills", "doomed", "SKILL.md")
	preApply, err := os.ReadFile(skillDest)
	if err != nil {
		t.Fatalf("first apply should have written the skill: %v", err)
	}

	// Drop the skill from source and enable backup: the second apply's ONLY
	// change is the orphan delete.
	if err := os.RemoveAll(filepath.Join(tmp, ".agentsync", "skills", "doomed")); err != nil {
		t.Fatal(err)
	}
	enableGitBackupOn(t, tmp)
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("delete-only apply: %v\n%s", err, out)
	}
	if _, err := os.Stat(skillDest); !os.IsNotExist(err) {
		t.Fatalf("apply should have removed the orphaned skill; stat err=%v", err)
	}

	// The deleted bytes must be recoverable: revert to the pre-apply baseline
	// restores the skill byte-for-byte.
	out, err := runCLI(t, env, "revert", "claude")
	if err != nil {
		t.Fatalf("revert: %v\n%s", err, out)
	}
	got, err := os.ReadFile(skillDest)
	if err != nil {
		t.Fatalf("revert did not restore the deleted skill (its bytes were in no baseline): %v\n%s", err, out)
	}
	if string(got) != string(preApply) {
		t.Fatalf("restored skill differs from pre-apply bytes:\n got %q\nwant %q", got, preApply)
	}
}

// TestApply_GitBackupBaselineStagesUntrackedPlannedPath pins the PRODUCTION
// call site of git.SnapshotPreApply (the already-owned branch of the baseline
// pass): a managed file that exists on disk but is UNTRACKED — it was written
// by an apply that ran with git backup off — is about to be overwritten, and
// its pre-apply bytes exist in no history. The baseline must stage them, or
// the overwrite is unrecoverable. Reverting the gitbackup.go call to
// SnapshotDirtyTracked leaves this red: tracked-dirt-only snapshots skip the
// untracked planned path.
func TestApply_GitBackupBaselineStagesUntrackedPlannedPath(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	enableGitBackupOn(t, tmp)

	// Apply 1 (backup on): skill A becomes tracked; the root is agentsync-owned.
	writeSkillSource(t, tmp, "tracked", "a1")
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 1: %v\n%s", err, out)
	}

	// Apply 2 (backup OFF): skill B lands on disk with bytes no history has.
	writeSkillSource(t, tmp, "untracked", "precious-b2")
	if out, err := runCLI(t, env, "apply", "--no-git-backup"); err != nil {
		t.Fatalf("apply 2: %v\n%s", err, out)
	}

	// Apply 3 (backup on): source changes B, so the apply overwrites the
	// untracked-but-planned file — SnapshotPreApply must baseline "precious-b2".
	writeSkillSource(t, tmp, "untracked", "b3-overwrites")
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply 3: %v\n%s", err, out)
	}
	skillB := filepath.Join(tmp, ".claude", "skills", "untracked", "SKILL.md")
	if b, _ := os.ReadFile(skillB); !strings.Contains(string(b), "b3-overwrites") {
		t.Fatalf("precondition: apply 3 should have overwritten skill B:\n%s", b)
	}

	// Revert: the pre-apply bytes of the untracked planned path must come back.
	if out, err := runCLI(t, env, "revert", "claude"); err != nil {
		t.Fatalf("revert: %v\n%s", err, out)
	}
	b, err := os.ReadFile(skillB)
	if err != nil {
		t.Fatalf("revert removed skill B entirely: %v", err)
	}
	if !strings.Contains(string(b), "precious-b2") {
		t.Fatalf("revert did not restore the untracked planned path's pre-apply bytes:\n%s", b)
	}
}
