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
