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
		URL: rawURL,
	}

	// Shallow-clone for speed, but ONLY when no exact commit is pinned. A
	// depth-1 clone fetches just the branch tip; checking out an older pinned
	// sha then fails with "object not found" because that commit was never
	// fetched (the symptom seen on chrome-devtools-mcp, whose marketplace entry
	// pins a sha that lags the branch head). A sha pin needs full history so the
	// commit object is present.
	if src.SHA == "" {
		cloneOpts.Depth = 1
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
		pullOpts := &git.PullOptions{}
		if src.SHA == "" {
			// Match the clone: stay shallow only when no exact sha is pinned, so
			// a pinned commit's object is present for the checkout below.
			pullOpts.Depth = 1
		}
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

	// go-git materializes committed symlinks on disk. A symlink whose target
	// ESCAPES the fetched tree (skills/x -> /etc) would let os.ReadFile follow
	// it off the cache and pull foreign host content into agent config — the
	// lexical component-path containment check cannot catch that, so refuse it
	// here. An IN-TREE symlink (e.g. superpowers' AGENTS.md -> CLAUDE.md) is
	// safe and is allowed, so a plugin that legitimately uses one is no longer
	// rejected wholesale. (The npm/relative fetchers still reject every symlink:
	// their copy mechanism cannot preserve a link, and tarballs/local trees have
	// no comparable legitimate use.) The .git dir is never projected, so skip it.
	if err := rejectEscapingSymlinks(into); err != nil {
		return FetchResult{}, err
	}

	return FetchResult{HeadSHA: headSHA}, nil
}

// rejectEscapingSymlinks walks root and returns an error for any symlink whose
// fully-resolved target lies outside root, skipping the .git directory. An
// in-tree symlink is permitted; an unresolvable (e.g. dangling) one is refused
// rather than guessed (fail closed).
func rejectEscapingSymlinks(root string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("git fetcher: resolve clone root %s: %w", root, err)
	}
	absRoot, err := filepath.Abs(resolvedRoot)
	if err != nil {
		return fmt.Errorf("git fetcher: abs clone root %s: %w", root, err)
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		// Fully resolve the link (this also defeats intermediate-symlink games)
		// and require the target to stay within the clone. EvalSymlinks needs
		// the target to exist; a dangling or otherwise unresolvable link is
		// refused rather than guessed.
		resolved, rerr := filepath.EvalSymlinks(path)
		if rerr != nil {
			return fmt.Errorf("git fetcher: cannot resolve symlink %q (refusing): %w", rel, rerr)
		}
		absResolved, rerr := filepath.Abs(resolved)
		if rerr != nil {
			return fmt.Errorf("git fetcher: abs symlink target for %q: %w", rel, rerr)
		}
		if !pathContains(absRoot, absResolved) {
			return fmt.Errorf("git fetcher: symlink %q points outside the plugin tree (refusing — would escape the cache)", rel)
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

	// Symlink-escape guard. go-git materializes committed symlinks, so subPath
	// (or an intermediate component) can be a symlink that the lexical
	// containment check above cannot catch and the os.Stat above happily
	// FOLLOWED. Resolve all symlinks and re-verify containment, then copy from
	// the RESOLVED path — otherwise a crafted git-subdir plugin could point its
	// subdir at /etc or ~/.ssh and slurp host files into the cache (and from
	// there into agent config). This runs before rejectEscapingSymlinks(into),
	// which only sees the post-extraction tree (subdir extraction copies via
	// copyDir, which rejects every symlink).
	resolvedSub, err := filepath.EvalSymlinks(fullSub)
	if err != nil {
		return fmt.Errorf("resolve subdir %s: %w", subPath, err)
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve clone root: %w", err)
	}
	if !pathContains(resolvedDir, resolvedSub) {
		return fmt.Errorf("subdir %q resolves outside the repository root via a symlink (refusing escape)", subPath)
	}

	tmp := dir + ".subdir_tmp"
	old := dir + ".subdir_old"

	// Best-effort: clean up any stragglers from a previous interrupted
	// extraction so we don't fail at the rename step below with EEXIST.
	_ = os.RemoveAll(tmp)
	_ = os.RemoveAll(old)

	if err := copyDir(resolvedSub, tmp); err != nil {
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
