package marketplace

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// GitFetcher handles "github", "url", and "git-subdir" source kinds.
// All three are git repositories; the difference is:
//   - "github": repo = "owner/repo", clone from https://github.com/<repo>
//   - "url":    repo or url = full git URL
//   - "git-subdir": same as github/url but also limits to a sub-directory path
type GitFetcher struct{}

// Fetch clones (or re-uses an already-cloned) repository into into, optionally
// checking out a specific ref/sha and, for git-subdir, extracting only the
// configured subdirectory.
func (f *GitFetcher) Fetch(src Source, into string) (FetchResult, error) {
	rawURL := resolveGitURL(src)
	if rawURL == "" {
		return FetchResult{}, fmt.Errorf("git fetcher: cannot determine repository URL from source %+v", src)
	}

	if err := os.MkdirAll(into, 0o755); err != nil {
		return FetchResult{}, fmt.Errorf("git fetcher: mkdir %s: %w", into, err)
	}

	cloneOpts := &git.CloneOptions{
		URL:   rawURL,
		Depth: 1,
	}

	// Attach a specific branch/tag ref when requested.
	if src.Ref != "" && src.SHA == "" {
		cloneOpts.ReferenceName = resolveRefName(src.Ref)
		cloneOpts.SingleBranch = true
	}

	repo, err := git.PlainClone(into, false, cloneOpts)
	if err == git.ErrRepositoryAlreadyExists {
		// Re-open existing clone and fetch latest.
		repo, err = git.PlainOpen(into)
		if err != nil {
			return FetchResult{}, fmt.Errorf("git fetcher: open existing repo: %w", err)
		}
		w, err := repo.Worktree()
		if err != nil {
			return FetchResult{}, fmt.Errorf("git fetcher: worktree: %w", err)
		}
		pullOpts := &git.PullOptions{Depth: 1}
		if src.Ref != "" && src.SHA == "" {
			pullOpts.ReferenceName = resolveRefName(src.Ref)
			pullOpts.SingleBranch = true
		}
		if err := w.Pull(pullOpts); err != nil && err != git.NoErrAlreadyUpToDate {
			return FetchResult{}, fmt.Errorf("git fetcher: pull: %w", err)
		}
	} else if err != nil {
		return FetchResult{}, fmt.Errorf("git fetcher: clone %s: %w", rawURL, err)
	}

	// If a specific SHA was requested, checkout that commit.
	if src.SHA != "" {
		w, err := repo.Worktree()
		if err != nil {
			return FetchResult{}, fmt.Errorf("git fetcher: worktree for sha checkout: %w", err)
		}
		hash := plumbing.NewHash(src.SHA)
		if err := w.Checkout(&git.CheckoutOptions{Hash: hash}); err != nil {
			return FetchResult{}, fmt.Errorf("git fetcher: checkout %s: %w", src.SHA, err)
		}
	}

	// Determine HEAD SHA.
	head, err := repo.Head()
	if err != nil {
		return FetchResult{}, fmt.Errorf("git fetcher: HEAD: %w", err)
	}
	headSHA := head.Hash().String()

	// For git-subdir: if a sub-path is specified, copy only that subtree out
	// to a temp location and replace the destination.
	if src.Kind == "git-subdir" && src.Path != "" {
		if err := extractSubdir(into, src.Path); err != nil {
			return FetchResult{}, fmt.Errorf("git fetcher: extract subdir %s: %w", src.Path, err)
		}
	}

	// go-git materializes committed symlinks on disk. Without this, a
	// malicious plugin repo could ship a symlink (skills/x -> /etc) that the
	// lexical component-path containment check cannot catch and os.ReadFile
	// would follow off the cache. Reject any symlink in the fetched tree —
	// the same stance npm/relative fetchers take. The .git dir is never
	// projected, so skip it.
	if err := rejectSymlinks(into); err != nil {
		return FetchResult{}, err
	}

	return FetchResult{HeadSHA: headSHA}, nil
}

// rejectSymlinks walks root and returns an error if any entry is a symlink,
// skipping the .git directory.
func rejectSymlinks(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			return fmt.Errorf("git fetcher: repository contains a symlink %q (refusing — plugin trees must contain only regular files and directories)", rel)
		}
		return nil
	})
}

// resolveGitURL returns the clone URL for the given source.
func resolveGitURL(src Source) string {
	switch src.Kind {
	case "github":
		if src.URL != "" {
			return src.URL
		}
		if src.Repo != "" {
			if strings.HasPrefix(src.Repo, "https://") || strings.HasPrefix(src.Repo, "file://") {
				return src.Repo
			}
			return "https://github.com/" + src.Repo
		}
	case "url", "git-subdir":
		if src.URL != "" {
			return src.URL
		}
		if src.Repo != "" {
			return src.Repo
		}
	}
	return ""
}

// resolveRefName converts a human ref string (branch name, tag, or full ref)
// to a plumbing.ReferenceName.
func resolveRefName(ref string) plumbing.ReferenceName {
	if strings.HasPrefix(ref, "refs/") {
		return plumbing.ReferenceName(ref)
	}
	// Try tag first; callers pass "v1.0" style tags commonly.
	// We default to treating it as a branch; go-git resolves either.
	return plumbing.NewBranchReferenceName(ref)
}

// extractSubdir replaces the contents of dir with only the subdirectory at
// subPath within dir. Files outside subPath are removed; files inside
// subPath are moved up to dir root.
//
// The strategy is rename-old-aside, rename-new-into-place, then cleanup:
//  1. copyDir(fullSub, tmp)            // populate the new layout
//  2. rename(dir, dir+".old")          // get the old clone out of the way
//  3. rename(tmp, dir)                 // put new content into place
//  4. RemoveAll(dir+".old")            // best-effort cleanup
//
// If step 3 fails (cross-device rename, antivirus locking on Windows), we
// undo step 2 to restore the old clone. The previous implementation did
// RemoveAll(dir) before Rename(tmp, dir) — if the rename then failed for
// any reason, the cache was permanently destroyed and subsequent fetches
// would either re-clone (fine) or fail confusingly because the cache dir
// existed but wasn't a git repo.
func extractSubdir(dir, subPath string) error {
	fullSub := filepath.Join(dir, subPath)
	// Containment guard: subPath comes straight from an untrusted
	// marketplace.json `path` field. A traversal sequence ("../../etc")
	// resolves OUTSIDE the clone after filepath.Join, which would let the
	// copyDir below slurp arbitrary host files into the plugin cache — and
	// from there into the user's agent config. Refuse anything that escapes
	// the clone root. (The sibling npm/relative fetchers already bound their
	// extraction; this closes the lone git-subdir hole.)
	if !pathContains(dir, fullSub) {
		return fmt.Errorf("subdir %q escapes the repository root (refusing path traversal)", subPath)
	}
	info, err := os.Stat(fullSub)
	if err != nil {
		return fmt.Errorf("subdir %s does not exist in clone: %w", subPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("subdir %s is not a directory", subPath)
	}

	tmp := dir + ".subdir_tmp"
	old := dir + ".subdir_old"

	// Best-effort: clean up any stragglers from a previous interrupted
	// extraction so we don't fail at the rename step below with EEXIST.
	_ = os.RemoveAll(tmp)
	_ = os.RemoveAll(old)

	if err := copyDir(fullSub, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("copy subdir to tmp: %w", err)
	}

	// Move the old clone aside (rename, not delete) so we can restore it
	// if the next step fails.
	if err := os.Rename(dir, old); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("rename clone aside: %w", err)
	}

	if err := os.Rename(tmp, dir); err != nil {
		// Restore the original — keep the cache in a usable state.
		_ = os.RemoveAll(tmp)
		if rErr := os.Rename(old, dir); rErr != nil {
			return fmt.Errorf("rename tmp→dir failed (%v) and rollback failed (%v); cache at %s",
				err, rErr, old)
		}
		return fmt.Errorf("rename tmp→dir: %w", err)
	}

	// New layout is in place. Remove the moved-aside original.
	_ = os.RemoveAll(old)
	return nil
}

// Note: go-git v5 does not support sparse checkout natively in PlainClone.
// For git-subdir, we perform a full clone and then trim via extractSubdir.
