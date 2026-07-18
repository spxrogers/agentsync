package git

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
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
	// Pre-flight the STRUCTURAL conflicts a create/modify can't resolve without
	// destroying a file agentsync doesn't manage, and refuse BEFORE mutating the
	// worktree — so no unmanaged file is ever deleted and these conflicts fail up
	// front rather than half-way through the delete pass. We inspect the worktree
	// filesystem directly (NOT git status, which omits gitignored files):
	//   (1) a path that is a file in the target but CURRENTLY a directory whose
	//       contents aren't all being deleted (it holds untracked/gitignored files)
	//       — replacing it with a file would delete them;
	//  (1b) a CREATE path that is currently a regular file — since a create means
	//       HEAD doesn't track this path, that file is the user's own
	//       (untracked/gitignored); overwriting + committing it would violate the
	//       untouched-files promise;
	//   (2) a create whose ancestor is an existing unmanaged FILE — MkdirAll would
	//       fail over it.
	// A non-transactional restore can't guarantee zero partial state for every
	// possible I/O error, but any staged-but-uncommitted delete is git-recoverable
	// and no unmanaged file is ever lost. restoreFileFromTree keeps a defensive
	// backstop for the same dir->file case.
	deleting := make(map[string]bool, len(changes))
	for _, ch := range changes {
		if ch.Kind == "delete" {
			deleting[ch.Path] = true
		}
	}
	for _, ch := range changes {
		if ch.Kind == "delete" {
			continue
		}
		abs := filepath.Join(r.dir, filepath.FromSlash(ch.Path))
		info, statErr := os.Stat(abs)
		switch {
		case statErr == nil && info.IsDir():
			blocker, werr := firstUnmanagedFileUnder(r.dir, abs, deleting)
			if werr != nil {
				return "", fmt.Errorf("scanning %s during revert in %s: %w", ch.Path, r.dir, werr)
			}
			if blocker != "" {
				return "", fmt.Errorf("cannot restore %q to a file: the directory holds files agentsync does not manage (e.g. %q); move or remove them, then re-run revert", ch.Path, blocker)
			}
		case statErr == nil && ch.Kind == "create":
			// (1b) a create over an existing regular file: it is the user's own
			// untracked/gitignored file (HEAD doesn't track this path) — refuse
			// rather than overwrite and commit it.
			return "", fmt.Errorf("cannot restore %q: a file agentsync does not manage already exists there; move or remove it, then re-run revert", ch.Path)
		}
		for parent := path.Dir(ch.Path); parent != "." && parent != "/"; parent = path.Dir(parent) {
			pinfo, statErr := os.Stat(filepath.Join(r.dir, filepath.FromSlash(parent)))
			if statErr != nil {
				continue // doesn't exist yet — MkdirAll will create it
			}
			if pinfo.IsDir() {
				break // a real directory — fine
			}
			if !deleting[parent] {
				return "", fmt.Errorf("cannot restore %q: its parent %q is a file agentsync does not manage; move or remove it, then re-run revert", ch.Path, parent)
			}
			break // parent is a file being deleted — the delete pass clears it first
		}
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

// firstUnmanagedFileUnder walks absDir and returns the first regular file — as a
// repoDir-relative slash path — that is NOT in the `deleting` set: an untracked or
// gitignored file that replacing this directory with a file would have to destroy.
// It returns "" when the directory holds nothing agentsync isn't already removing.
func firstUnmanagedFileUnder(repoDir, absDir string, deleting map[string]bool) (string, error) {
	var found string
	err := filepath.WalkDir(absDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(repoDir, p)
		if rerr != nil {
			return rerr
		}
		if slash := filepath.ToSlash(rel); !deleting[slash] {
			found = slash
			return filepath.SkipAll // stop at the first unmanaged file
		}
		return nil
	})
	return found, err
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
	// Drop any existing file so the target mode takes effect on (re)create. Restore's
	// up-front pre-flight already refuses a dir->file replacement that would destroy
	// unmanaged files, so reaching here with p still a non-empty directory means the
	// filesystem changed AFTER that scan (a concurrent write) — a TOCTOU backstop.
	// Distinct wording ("still holds ... after the pre-flight") so a test can tell it
	// apart from the pre-flight's refusal; we still never delete the unmanaged files.
	if err := fs.Remove(p); err != nil && !os.IsNotExist(err) {
		if info, statErr := fs.Stat(p); statErr == nil && info.IsDir() {
			return fmt.Errorf("cannot restore %q to a file: its directory still holds unmanaged files after the pre-flight (a concurrent change?); move or remove your own files under %q, then re-run revert", p, p)
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
