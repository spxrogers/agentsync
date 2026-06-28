package git

import (
	"errors"
	"fmt"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// Checkpoint is one commit in a destination repo's history.
type Checkpoint struct {
	Hash    string    // full hash
	Short   string    // first 7 chars of Hash
	Subject string    // first line of the commit message
	When    time.Time // author time
}

// Log returns up to n checkpoints, newest first. n <= 0 returns the full history.
func (r *Repo) Log(n int) ([]Checkpoint, error) {
	iter, err := r.repo.Log(&gogit.LogOptions{})
	if err != nil {
		// A repo with no commits yet has no HEAD; report an empty history rather
		// than an error so callers can say "nothing to revert to".
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("git log in %s: %w", r.dir, err)
	}
	defer iter.Close()
	var out []Checkpoint
	// ForEach returns nil when the callback returns storer.ErrStop, so this is the
	// clean way to cap the walk at n without treating "done" as an error.
	err = iter.ForEach(func(c *object.Commit) error {
		out = append(out, Checkpoint{
			Hash:    c.Hash.String(),
			Short:   shortHash(c.Hash),
			Subject: strings.SplitN(c.Message, "\n", 2)[0],
			When:    c.Author.When,
		})
		if n > 0 && len(out) >= n {
			return storer.ErrStop
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking git log in %s: %w", r.dir, err)
	}
	return out, nil
}

// Resolve turns a revision (e.g. "HEAD", "HEAD~1", a short or full hash) into a
// full commit hash, erroring clearly when it does not resolve.
func (r *Repo) Resolve(rev string) (string, error) {
	h, err := r.repo.ResolveRevision(plumbing.Revision(rev))
	if err != nil {
		return "", fmt.Errorf("no such checkpoint %q in %s: %w", rev, r.dir, err)
	}
	return h.String(), nil
}

// HasMultipleCheckpoints reports whether the repo has at least two commits, so a
// default "undo the most recent apply" (HEAD~1) has somewhere to land.
func (r *Repo) HasMultipleCheckpoints() (bool, error) {
	cps, err := r.Log(2)
	if err != nil {
		return false, err
	}
	return len(cps) >= 2, nil
}

// headHash returns the current HEAD commit hash.
func (r *Repo) headHash() (plumbing.Hash, error) {
	ref, err := r.repo.Head()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolving HEAD in %s: %w", r.dir, err)
	}
	return ref.Hash(), nil
}

// shortHash renders the conventional 7-char abbreviation.
func shortHash(h plumbing.Hash) string {
	s := h.String()
	if len(s) >= 7 {
		return s[:7]
	}
	return s
}
