package marketplace

import (
	"fmt"
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

	return FetchResult{HeadSHA: headSHA}, nil
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
// subPath within dir. Files outside subPath are removed; files inside subPath
// are moved up to dir root.
func extractSubdir(dir, subPath string) error {
	fullSub := filepath.Join(dir, subPath)
	info, err := os.Stat(fullSub)
	if err != nil {
		return fmt.Errorf("subdir %s does not exist in clone: %w", subPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("subdir %s is not a directory", subPath)
	}

	// Copy subdirectory to a temp location alongside dir.
	tmp := dir + ".subdir_tmp"
	if err := copyDir(fullSub, tmp); err != nil {
		return fmt.Errorf("copy subdir to tmp: %w", err)
	}

	// Remove original clone.
	if err := os.RemoveAll(dir); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("remove clone dir: %w", err)
	}

	// Rename tmp → dir.
	return os.Rename(tmp, dir)
}

// Note: go-git v5 does not support sparse checkout natively in PlainClone.
// For git-subdir, we perform a full clone and then trim via extractSubdir.
