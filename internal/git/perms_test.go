package git

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestEnsureDirSecureRetightensLoosened is the on-disk fidelity test for issue #126's
// re-tighten control: a loosened `.git` (0o755 / 0o777) is best-effort re-chmod'd back
// to 0o700 and a warning is reported, while an already-0o700 `.git` fires no warning.
func TestEnsureDirSecureRetightensLoosened(t *testing.T) {
	testenv.RequireContainer(t)
	if runtime.GOOS == "windows" {
		t.Skip("POSIX perm bits are meaningless on Windows")
	}
	cases := []struct {
		name     string
		mode     os.FileMode
		wantWarn bool
	}{
		{name: "already-0700", mode: 0o700, wantWarn: false},
		{name: "loosened-0755", mode: 0o755, wantWarn: true},
		{name: "loosened-0777", mode: 0o777, wantWarn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r, err := Init(dir)
			if err != nil {
				t.Fatal(err)
			}
			gitDir := filepath.Join(dir, ".git")
			if err := os.Chmod(gitDir, tc.mode); err != nil {
				t.Fatal(err)
			}
			var warned []string
			r.EnsureDirSecure(func(msg string) { warned = append(warned, msg) })

			// On-disk .git is 0o700 afterward regardless of how loose it started.
			info, err := os.Stat(gitDir)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Errorf(".git mode = %o after EnsureDirSecure, want 700", got)
			}
			switch {
			case tc.wantWarn && len(warned) == 0:
				t.Errorf("expected a loosened-history warning, got none")
			case !tc.wantWarn && len(warned) != 0:
				t.Errorf("expected no warning for an already-0700 .git, got %v", warned)
			}
		})
	}
}

// TestInitNoticeAlwaysPresentForOwnedRepo pins the atomicity invariant (issue #126): a
// repo that Detects as StateAgentsyncOwned ALWAYS carries the local-history NOTICE.
// When the notice write fails, the half-created .git is rolled back so Detect reverts
// to StateUntracked — never a marked-but-noticeless cleartext-secret repo.
func TestInitNoticeAlwaysPresentForOwnedRepo(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	// Force the NoticeFile write to fail by pre-creating its path as a directory.
	if err := os.MkdirAll(filepath.Join(dir, NoticeFile), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := Init(dir); err == nil {
		t.Fatal("Init should fail when the NoticeFile cannot be written")
	}
	st, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st == StateAgentsyncOwned {
		t.Fatalf("a failed-notice Init left the repo StateAgentsyncOwned; want it rolled back to StateUntracked "+
			"(owned must always carry the notice), got %v", st)
	}
	// The freshly-created .git was rolled back, so the next apply retries cleanly.
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git should have been rolled back after the notice-write failure; stat err=%v", err)
	}
}

// TestInitTightensGitDir asserts the on-disk `.git` mode is 0o700 immediately after a
// successful Init (the init-time control that EnsureDirSecure later re-asserts).
func TestInitTightensGitDir(t *testing.T) {
	testenv.RequireContainer(t)
	if runtime.GOOS == "windows" {
		t.Skip("POSIX perm bits are meaningless on Windows")
	}
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf(".git mode = %o after Init, want 700", got)
	}
}

// TestGitPermLifecycle exercises the full #126 `.git` permission lifecycle end to
// end — Init → external chmod drift → a real checkpoint commit through the loosened
// dir → EnsureDirSecure re-tighten — which the narrower TestInitTightensGitDir
// (no drift/commit) and TestEnsureDirSecureRetightensLoosened (no commit) do not
// combine. It pins two facts about where the re-tighten actually lives:
//
//   - the low-level commit primitives (Stage + CommitStaged) do NOT re-tighten
//     `.git`; they commit successfully through a loosened dir but leave its perms
//     as they found them;
//   - EnsureDirSecure — the control the CLI commit path invokes (internal/cli/
//     gitbackup.go) — is what re-tightens the loosened `.git` back to 0o700 and
//     surfaces a warning.
//
// The Windows branch of tightenGitDir is a compile-guarded no-op; POSIX perm bits
// are meaningless there, so this skips on Windows like the sibling perm tests.
func TestGitPermLifecycle(t *testing.T) {
	testenv.RequireContainer(t)
	if runtime.GOOS == "windows" {
		t.Skip("POSIX perm bits are meaningless on Windows")
	}
	dir := t.TempDir()
	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(dir, ".git")

	// (a) Init tightens .git to 0o700.
	if got := statPerm(t, gitDir); got != 0o700 {
		t.Fatalf(".git mode = %o after Init, want 700", got)
	}

	// (b) External drift loosens .git (a git gc, a restore-from-tar, an admin chmod).
	if err := os.Chmod(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// (c) A real checkpoint commits cleanly through the loosened .git.
	writeFile(t, dir, "rendered.json", `{"token":"cleartext"}`)
	if err := r.Stage([]string{"rendered.json"}); err != nil {
		t.Fatalf("stage through loosened .git: %v", err)
	}
	hash, err := r.CommitStaged("checkpoint", DefaultIdentity)
	if err != nil {
		t.Fatalf("commit through loosened .git: %v", err)
	}
	if hash == "" {
		t.Fatal("commit through loosened .git returned an empty hash")
	}
	// The commit primitive itself does NOT re-tighten — the drift is still present.
	if got := statPerm(t, gitDir); got != 0o755 {
		t.Fatalf(".git mode = %o after a bare commit, want it left at 755 "+
			"(re-tighten is EnsureDirSecure's job, not the commit primitive's)", got)
	}

	// (d) EnsureDirSecure (the #126 control the CLI commit path invokes) re-tightens
	//     the loosened .git to 0o700 and warns about the drifted cleartext history.
	var warned []string
	r.EnsureDirSecure(func(msg string) { warned = append(warned, msg) })
	if got := statPerm(t, gitDir); got != 0o700 {
		t.Errorf(".git mode = %o after EnsureDirSecure, want 700", got)
	}
	if len(warned) == 0 {
		t.Error("EnsureDirSecure should warn when it re-tightens a loosened cleartext-secret history")
	}
}

// statPerm returns the permission bits of path, failing the test on a stat error.
func statPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
