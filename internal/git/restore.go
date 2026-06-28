package git

import (
	"fmt"
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

// Restore makes the worktree match the targetRev checkpoint and records the result
// as a NEW commit on top of the current HEAD. It is append-only: HEAD advances,
// nothing is rewritten or lost, so the bad apply stays in history and the revert
// is itself revertible. Returns the new commit hash, or ("", nil) when the worktree
// already matches target (no checkpoint recorded).
func (r *Repo) Restore(targetRev, message string, id Identity) (string, error) {
	targetStr, changes, err := r.Plan(targetRev)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", nil
	}
	orig, err := r.headHash()
	if err != nil {
		return "", err
	}
	wt, err := r.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("worktree for %s: %w", r.dir, err)
	}
	// Append-only restore via two resets:
	//   1. HARD reset to target  → index + worktree now hold target's content
	//      (and the branch ref transiently points at target).
	//   2. SOFT reset to orig    → moves the branch ref back to the original HEAD
	//      WITHOUT touching the index/worktree (go-git SoftReset leaves both).
	// Committing now records target's content as a new commit whose PARENT is the
	// original HEAD — so the intervening commits (and the bad apply) stay reachable.
	if err := wt.Reset(&gogit.ResetOptions{Commit: plumbing.NewHash(targetStr), Mode: gogit.HardReset}); err != nil {
		return "", fmt.Errorf("reset worktree to checkpoint %s in %s: %w", shortStr(targetStr), r.dir, err)
	}
	if err := wt.Reset(&gogit.ResetOptions{Commit: orig, Mode: gogit.SoftReset}); err != nil {
		return "", fmt.Errorf("restoring branch ref after revert in %s: %w", r.dir, err)
	}
	sig := signature(id)
	h, err := wt.Commit(message, &gogit.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		return "", fmt.Errorf("recording revert commit in %s: %w", r.dir, err)
	}
	return h.String(), nil
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
