package git

import (
	"fmt"
	"sort"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// now is the commit-timestamp source. It is a package var so tests can pin it;
// the timestamp is purely informational (it feeds no content hash or comparison),
// so a direct time.Now() is fine here — the render/state time.Now() caveat does
// not apply to this package.
var now = func() time.Time { return time.Now().UTC() }

// Stage adds each of relPaths (slash-relative to the repo root) to the index. A
// path that is gone from the worktree is staged as a deletion if it is tracked.
func (r *Repo) Stage(relPaths []string) error {
	wt, err := r.repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree for %s: %w", r.dir, err)
	}
	for _, rel := range relPaths {
		if _, err := wt.Add(rel); err != nil {
			return fmt.Errorf("git add %s: %w", rel, err)
		}
	}
	return nil
}

// StageTrackedDeletions stages the removal of any already-TRACKED file that is now
// missing from the worktree — so an apply that deleted a managed file (e.g. a
// dropped MCP server) records that deletion in the checkpoint. It deliberately
// does NOT touch untracked files (a `?` status) the user may have dropped into the
// dir: agentsync only versions what it wrote. Returns the staged paths.
func (r *Repo) StageTrackedDeletions() ([]string, error) {
	wt, err := r.repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("worktree for %s: %w", r.dir, err)
	}
	st, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("git status in %s: %w", r.dir, err)
	}
	var staged []string
	for path, fs := range st {
		if fs.Worktree == gogit.Deleted {
			if _, err := wt.Add(path); err != nil {
				return nil, fmt.Errorf("git add (delete) %s: %w", path, err)
			}
			staged = append(staged, path)
		}
	}
	return staged, nil
}

// CommitStaged records one commit of the current index authored by id, or returns
// ("", nil) when the index has nothing to commit (so an apply that changed nothing
// produces no empty checkpoint).
func (r *Repo) CommitStaged(message string, id Identity) (string, error) {
	wt, err := r.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("worktree for %s: %w", r.dir, err)
	}
	clean, err := indexClean(wt)
	if err != nil {
		return "", err
	}
	if clean {
		return "", nil
	}
	sig := signature(id)
	h, err := wt.Commit(message, &gogit.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		return "", fmt.Errorf("git commit in %s: %w", r.dir, err)
	}
	return h.String(), nil
}

// SnapshotDirtyTracked commits any uncommitted changes to already-TRACKED files
// (modifications and deletions) as a safety checkpoint, so a subsequent
// destructive operation (notably revert's hard reset) cannot lose them. Untracked
// files — the user's own scratch files — are never touched. Returns the snapshot
// commit hash, or ("", nil) when the worktree has no tracked changes. This is what
// keeps the append-only promise true of the WORKTREE, not just of history.
func (r *Repo) SnapshotDirtyTracked(message string, id Identity) (string, error) {
	wt, err := r.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("worktree for %s: %w", r.dir, err)
	}
	st, err := wt.Status()
	if err != nil {
		return "", fmt.Errorf("git status in %s: %w", r.dir, err)
	}
	var dirty []string
	for path, fs := range st {
		if isUntracked(fs) {
			continue // the user's own files — never snapshot or touch them
		}
		if fs.Worktree != gogit.Unmodified || fs.Staging != gogit.Unmodified {
			dirty = append(dirty, path)
		}
	}
	if len(dirty) == 0 {
		return "", nil
	}
	sort.Strings(dirty)
	for _, p := range dirty {
		if _, err := wt.Add(p); err != nil {
			return "", fmt.Errorf("git add %s: %w", p, err)
		}
	}
	return r.CommitStaged(message, id)
}

// IsClean reports whether the worktree has no uncommitted changes to TRACKED files
// (untracked files are ignored — agentsync never commits them).
func (r *Repo) IsClean() (bool, error) {
	wt, err := r.repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("worktree for %s: %w", r.dir, err)
	}
	st, err := wt.Status()
	if err != nil {
		return false, fmt.Errorf("git status in %s: %w", r.dir, err)
	}
	for _, fs := range st {
		if isUntracked(fs) {
			continue
		}
		if fs.Worktree != gogit.Unmodified || fs.Staging != gogit.Unmodified {
			return false, nil
		}
	}
	return true, nil
}

// isUntracked reports whether a status entry is a purely-untracked file (go-git
// marks both columns Untracked for these).
func isUntracked(fs *gogit.FileStatus) bool {
	return fs.Worktree == gogit.Untracked && fs.Staging == gogit.Untracked
}

// signature builds the object.Signature for a checkpoint commit.
func signature(id Identity) *object.Signature {
	id = id.orDefault()
	return &object.Signature{Name: id.Name, Email: id.Email, When: now()}
}

// indexClean reports whether the staging area has nothing to commit (matches HEAD).
func indexClean(wt *gogit.Worktree) (bool, error) {
	st, err := wt.Status()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	// A repo is clean for commit purposes when no path has a staged change. We
	// can't use st.IsClean() alone (it also reports untracked files as "not
	// clean", which we intentionally never commit), so check the staging column.
	for _, fs := range st {
		if fs.Staging != gogit.Unmodified && fs.Staging != gogit.Untracked {
			return false, nil
		}
	}
	return true, nil
}
