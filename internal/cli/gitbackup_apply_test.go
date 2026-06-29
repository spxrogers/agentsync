package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	agit "github.com/spxrogers/agentsync/internal/git"
)

// TestApply_GitBackupCheckpoint is the end-to-end apply-tail test: with git backup
// enabled, the first apply that writes under ~/.claude initializes a local repo and
// records exactly one checkpoint; a re-apply with no source change records none.
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
	if len(cps) != 1 {
		t.Fatalf("want exactly 1 checkpoint after first apply, got %d", len(cps))
	}

	// Re-apply with no source change → no new checkpoint (apply is a no-op).
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("re-apply: %v\n%s", err, out)
	}
	cps2, _ := repo.Log(0)
	if len(cps2) != 1 {
		t.Fatalf("re-apply recorded a checkpoint despite no change: %d commits", len(cps2))
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

// TestDoctor_WarnsUnknownGitBackupMode checks doctor flags a typo'd mode value.
func TestDoctor_WarnsUnknownGitBackupMode(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	f, _ := os.OpenFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"), os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("\n[destination_directory_git_backup]\nmode = \"bogus\"\n")
	_ = f.Close()
	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "unknown value") || !strings.Contains(out, "bogus") {
		t.Fatalf("doctor should warn on the unknown mode; got:\n%s", out)
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
