package git

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// tightenGitDir stat-checks gitDir and, on POSIX, best-effort re-tightens it to
// 0o700 when its perms have drifted looser. It returns:
//   - changed: the perms were not already exactly 0o700 (a loosened cleartext-secret
//     history, or a fresh dir the umask left looser) — the caller may warn;
//   - err: a stat or re-tighten error (still best-effort — callers never abort on it).
//
// On Windows it is always (false, nil): POSIX mode bits are meaningless there, so a
// chmod would be a no-op that only produces noise. Non-recursive by design — a 0o700
// directory is the control (its contents are unreadable without it).
func tightenGitDir(gitDir string) (changed bool, err error) {
	if runtime.GOOS == "windows" {
		return false, nil
	}
	info, err := os.Stat(gitDir)
	if err != nil {
		return false, err
	}
	if info.Mode().Perm() == 0o700 {
		return false, nil
	}
	return true, os.Chmod(gitDir, 0o700)
}

// EnsureDirSecure re-asserts the at-rest control that makes agentsync's blessed
// local-only cleartext-secret history acceptable: the repo's `.git` dir must be
// 0o700. It is called on every apply's Open/commit path (and on fresh inits), so a
// history whose perms drifted looser — a `git gc`, a restore-from-tar without mode
// bits, an admin `chmod` — is best-effort re-tightened rather than left
// world/group-readable indefinitely while every apply writes fresh secret blobs into
// it. It also surfaces an init-time chmod that silently failed.
//
// It is BEST-EFFORT and NON-RECURSIVE and never aborts the apply. All reporting goes
// through the injected warn callback (so internal/git stays free of ui.Printer); a nil
// warn silences it. On Windows it is a no-op. A warning fires exactly when the `.git`
// perms were not already 0o700 (whether or not the re-tighten then succeeds).
func (r *Repo) EnsureDirSecure(warn func(string)) {
	gitDir := filepath.Join(r.dir, ".git")
	changed, err := tightenGitDir(gitDir)
	if !changed && err == nil {
		return // already 0o700 (or Windows) — nothing to report
	}
	if warn == nil {
		return
	}
	if err != nil {
		warn(fmt.Sprintf("could not re-tighten %s to 0700 (its history may hold cleartext secrets): %v", gitDir, err))
		return
	}
	warn(fmt.Sprintf("%s was looser than 0700 (its history may hold cleartext secrets); re-tightened it to 0700", gitDir))
}
