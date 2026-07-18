package git

import (
	"fmt"
	"io"
	"os"
	"path"
	"sort"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// FileChange describes one file a restore would touch, for `revert --dry-run`.
type FileChange struct {
	Path string
	Kind string // "create" | "modify" | "delete"
}

// Plan computes what Restore(targetRev) would change in the worktree relative to
// the current HEAD, WITHOUT writing anything. Returns the resolved target hash and
// the file changes (sorted by path).
func (r *Repo) Plan(targetRev string) (targetHash string, changes []FileChange, err error) {
	targetStr, err := r.Resolve(targetRev)
	if err != nil {
		return "", nil, err
	}
	headH, err := r.headHash()
	if err != nil {
		return "", nil, err
	}
	headTree, err := r.commitTree(headH)
	if err != nil {
		return "", nil, err
	}
	targetTree, err := r.commitTree(plumbing.NewHash(targetStr))
	if err != nil {
		return "", nil, err
	}
	diff, err := headTree.Diff(targetTree)
	if err != nil {
		return "", nil, fmt.Errorf("diffing checkpoints in %s: %w", r.dir, err)
	}
	var out []FileChange
	for _, ch := range diff {
		action, aerr := ch.Action()
		if aerr != nil {
			return "", nil, fmt.Errorf("classifying change in %s: %w", r.dir, aerr)
		}
		switch action {
		case merkletrie.Insert:
			out = append(out, FileChange{Path: ch.To.Name, Kind: "create"})
		case merkletrie.Delete:
			out = append(out, FileChange{Path: ch.From.Name, Kind: "delete"})
		default:
			out = append(out, FileChange{Path: ch.To.Name, Kind: "modify"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return targetStr, out, nil
}

// Restore makes the worktree's TRACKED files match the targetRev checkpoint and
// records the result as a NEW commit on top of the current HEAD. It is append-only:
// HEAD advances, nothing is rewritten or lost, so the bad apply stays in history
// and the revert is itself revertible. Returns the new commit hash, or ("", nil)
// when the worktree already matches target (no checkpoint recorded).
//
// It applies ONLY the tracked HEAD↔target delta (the FileChanges from Plan) to the
// worktree — it deliberately does NOT use go-git's HardReset. Unlike `git reset
// --hard`, go-git's HardReset enumerates and DELETES every untracked and gitignored
// worktree file; a revert sold as safe rollback must never destroy the user's own
// scratch files. Because only the diffed paths are touched, any untracked or
// gitignored file the user dropped into the dir survives byte-for-byte and stays
// untracked. (Callers snapshot uncommitted TRACKED edits first — see revert's
// SnapshotDirtyTracked — so at entry tracked worktree == HEAD and applying the
// delta reproduces target's tracked content exactly.)
func (r *Repo) Restore(targetRev, message string, id Identity) (string, error) {
	targetStr, changes, err := r.Plan(targetRev)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", nil
	}
	targetTree, err := r.commitTree(plumbing.NewHash(targetStr))
	if err != nil {
		return "", err
	}
	wt, err := r.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("worktree for %s: %w", r.dir, err)
	}
	// Apply the delta path-by-path and stage each change. HEAD never moves, so the
	// commit below is parented on the original HEAD automatically — the intervening
	// commits (and the bad apply) stay reachable, keeping revert append-only.
	//
	// Deletes are applied in a FIRST pass, before any create/modify, so a path that
	// swaps between a file and a directory across the two checkpoints cannot abort
	// mid-restore. E.g. going back to a target where "a" is a file while HEAD has
	// "a/b": the diff is {delete "a/b", create "a"}; removing "a/b" first empties
	// dir "a" so restoreFileFromTree can replace it with the file "a". The reverse
	// (delete file "a", then create "a/b") likewise needs the delete first so
	// MkdirAll("a") succeeds. Delete and create paths are otherwise disjoint (a tree
	// diff never both deletes and creates the same path), so the two passes are
	// order-independent within themselves.
	for _, ch := range changes {
		if ch.Kind != "delete" {
			continue
		}
		// wt.Remove both deletes the worktree file and stages the deletion.
		if _, err := wt.Remove(ch.Path); err != nil {
			return "", fmt.Errorf("removing %s during revert in %s: %w", ch.Path, r.dir, err)
		}
	}
	for _, ch := range changes {
		if ch.Kind == "delete" {
			continue // "create" | "modify"
		}
		if err := restoreFileFromTree(wt, targetTree, ch.Path); err != nil {
			return "", fmt.Errorf("restoring %s during revert in %s: %w", ch.Path, r.dir, err)
		}
		if _, err := wt.Add(ch.Path); err != nil {
			return "", fmt.Errorf("staging %s during revert in %s: %w", ch.Path, r.dir, err)
		}
	}
	sig := signature(id)
	h, err := wt.Commit(message, &gogit.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		return "", fmt.Errorf("recording revert commit in %s: %w", r.dir, err)
	}
	return h.String(), nil
}

// restoreFileFromTree writes the target tree's blob for slash-path p into the
// worktree filesystem, preserving the blob's file mode (notably the exec bit).
// It writes through wt.Filesystem so paths resolve under the worktree root even
// for folded/rerooted repos. The file is removed first so OpenFile's perm applies
// even when p already exists (a "modify"), since OpenFile only honors perm on
// creation.
//
// A symlink-mode blob (git mode 120000) is written as a regular file here, not
// recreated as a symlink; agentsync writes only regular files into the versioned
// dirs, so a tracked symlink blob does not arise in practice.
func restoreFileFromTree(wt *gogit.Worktree, tree *object.Tree, p string) error {
	fs := wt.Filesystem
	f, err := tree.File(p)
	if err != nil {
		return fmt.Errorf("loading blob %s from target checkpoint: %w", p, err)
	}
	mode, err := f.Mode.ToOSFileMode()
	if err != nil {
		return fmt.Errorf("resolving file mode of %s: %w", p, err)
	}
	reader, err := f.Reader()
	if err != nil {
		return fmt.Errorf("reading blob %s: %w", p, err)
	}
	defer reader.Close()

	if dir := path.Dir(p); dir != "." && dir != "/" {
		if err := fs.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating parent dir of %s: %w", p, err)
		}
	}
	// Drop any existing file so the target mode takes effect on (re)create. When p
	// was a DIRECTORY in HEAD and is a file in the target, the delete pass already
	// removed p's tracked contents; if the user left untracked files under p the
	// dir is still non-empty, and we must NOT delete them — so surface a clear,
	// actionable error rather than a raw "directory not empty".
	if err := fs.Remove(p); err != nil && !os.IsNotExist(err) {
		if info, statErr := fs.Stat(p); statErr == nil && info.IsDir() {
			return fmt.Errorf("cannot restore %q to a file: the directory still holds files agentsync does not manage; move or remove your own files under %q, then re-run revert", p, p)
		}
		return fmt.Errorf("replacing %s: %w", p, err)
	}
	dst, err := fs.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("opening %s for write: %w", p, err)
	}
	if _, err := io.Copy(dst, reader); err != nil {
		_ = dst.Close()
		return fmt.Errorf("writing %s: %w", p, err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", p, err)
	}
	return nil
}

// UntrackedPaths returns the sorted slash-relative paths of files in the worktree
// that git is not tracking (a `?` status) — the user's own scratch files that a
// revert leaves untouched. Note go-git's status does not enumerate gitignored
// files, so those (also preserved by revert) do not appear here.
func (r *Repo) UntrackedPaths() ([]string, error) {
	wt, err := r.repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("worktree for %s: %w", r.dir, err)
	}
	st, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("git status in %s: %w", r.dir, err)
	}
	var out []string
	for p, fs := range st {
		if isUntracked(fs) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// commitTree loads the tree of a commit hash.
func (r *Repo) commitTree(h plumbing.Hash) (*object.Tree, error) {
	c, err := r.repo.CommitObject(h)
	if err != nil {
		return nil, fmt.Errorf("loading commit %s in %s: %w", shortStr(h.String()), r.dir, err)
	}
	t, err := c.Tree()
	if err != nil {
		return nil, fmt.Errorf("loading tree of %s in %s: %w", shortStr(h.String()), r.dir, err)
	}
	return t, nil
}

// shortStr abbreviates a hex hash string to 7 chars for messages.
func shortStr(s string) string {
	if len(s) >= 7 {
		return s[:7]
	}
	return s
}
