// Package git is agentsync's local-only destination-versioning helper. It wraps
// go-git to give each rendered destination agent dir (~/.claude, ~/.codex, …) its
// own git repo so a bad `agentsync apply` is a quick rollback away (issue #118).
//
// These repos are a LOCAL ROLLBACK HISTORY and are NEVER pushed to a remote. The
// rendered files they version contain secrets resolved to cleartext at apply time;
// keeping the repos local-only is what makes that acceptable (the canonical
// ~/.agentsync/ source — the thing users commit and push — only ever holds secret
// references). Accordingly this package exposes NO remote/push surface at all:
// there is no function that adds a remote or pushes, and TestNoPushSurface guards
// that the surface never grows one. Do not add one.
package git

import (
	"errors"
	"fmt"

	gogit "github.com/go-git/go-git/v5"
)

// Identity is the author/committer recorded on checkpoint commits. agentsync uses
// a dedicated identity (not the user's git identity) so machine-authored
// checkpoints are distinct and committing works even when no global git identity
// is configured.
type Identity struct {
	Name  string
	Email string
}

// DefaultIdentity is used when the config supplies no override.
var DefaultIdentity = Identity{Name: "agentsync", Email: "agentsync@localhost"}

// orDefault fills empty fields from DefaultIdentity.
func (id Identity) orDefault() Identity {
	if id.Name == "" {
		id.Name = DefaultIdentity.Name
	}
	if id.Email == "" {
		id.Email = DefaultIdentity.Email
	}
	return id
}

// State classifies how a destination dir is tracked, so the apply tail knows
// whether to init, commit, or stay out of the user's way.
type State int

const (
	// StateUntracked: no git work tree at or above the dir — eligible for init.
	StateUntracked State = iota
	// StateAgentsyncOwned: a work tree agentsync created (carries the marker).
	StateAgentsyncOwned
	// StateForeign: a work tree the user owns (no marker) — leave it alone.
	StateForeign
)

// String renders the state for diagnostics/doctor.
func (s State) String() string {
	switch s {
	case StateAgentsyncOwned:
		return "agentsync-versioned"
	case StateForeign:
		return "foreign source control"
	default:
		return "untracked"
	}
}

// The marker lives in the repo's own .git/config as [agentsync] managed = true.
// It is how Detect tells a repo agentsync auto-created from one the user keeps
// (e.g. ~/.claude in their dotfiles), so agentsync only ever auto-commits into
// its own repos.
const (
	markerSection = "agentsync"
	markerOption  = "managed"
	markerValue   = "true"
)

// Detect reports how dir is tracked. It uses DetectDotGit so a destination nested
// inside an existing repo (a dotfiles user who keeps ~/.claude under git) is
// reported StateForeign rather than StateUntracked — agentsync must not init a
// nested repo or commit into someone else's history.
func Detect(dir string) (State, error) {
	repo, err := gogit.PlainOpenWithOptions(dir, &gogit.PlainOpenOptions{DetectDotGit: true})
	if errors.Is(err, gogit.ErrRepositoryNotExists) {
		return StateUntracked, nil
	}
	if err != nil {
		return StateUntracked, fmt.Errorf("opening git repo at %s: %w", dir, err)
	}
	owned, err := hasMarker(repo)
	if err != nil {
		return StateForeign, err
	}
	if owned {
		return StateAgentsyncOwned, nil
	}
	return StateForeign, nil
}

// hasMarker reports whether repo's config carries the agentsync-managed marker.
func hasMarker(repo *gogit.Repository) (bool, error) {
	cfg, err := repo.Config()
	if err != nil {
		return false, fmt.Errorf("reading git config: %w", err)
	}
	return cfg.Raw.Section(markerSection).Option(markerOption) == markerValue, nil
}

// Repo wraps a go-git repository plus its absolute work-tree root.
type Repo struct {
	dir  string
	repo *gogit.Repository
}

// Dir returns the repo's work-tree root.
func (r *Repo) Dir() string { return r.dir }

// Open opens an existing repo at dir (an exact .git at dir, not a nested parent).
// Callers gate on Detect first; Open does not re-check the marker.
func Open(dir string) (*Repo, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return nil, fmt.Errorf("opening git repo at %s: %w", dir, err)
	}
	return &Repo{dir: dir, repo: repo}, nil
}
