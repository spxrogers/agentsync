package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/spxrogers/agentsync/internal/iox"
)

// LastRunFile is the basename of the per-machine run record inside .state/.
const LastRunFile = "last-run.json"

// LastRun records what the last agentsync invocation on this machine was, so a
// release can tell a user once — and only once — what changed under them.
//
// It is deliberately a SEPARATE file from targets.json rather than a field on
// Targets, for three reasons:
//
//   - targets.json is drift state guarded by SchemaVersion; a UX marker has no
//     business gating on (or bumping) that version.
//   - targets.json is written only by the MUTATING commands. A read-only
//     `status` must be able to record that it showed the notice, or the notice
//     repeats forever for a user whose daily loop never applies.
//   - a corrupt or unwritable run record must never be able to take the drift
//     state down with it. Callers treat every error here as "skip the notice".
//
// It lives under .state/, which `init` gitignores, so a committed dotfiles repo
// never carries one machine's run record to another.
type LastRun struct {
	// Version is the agentsync version that last ran on this machine. Empty on
	// a record written by a build with no version stamped.
	Version string `json:"version"`
	// NoticesSeen holds the ids of the upgrade notices already shown, so a
	// version jump that skips releases still shows each notice exactly once and
	// a re-install never re-shows one.
	NoticesSeen []string `json:"notices_seen,omitempty"`
}

// Seen reports whether the notice id has already been shown on this machine.
func (l *LastRun) Seen(id string) bool {
	if l == nil {
		return false
	}
	for _, s := range l.NoticesSeen {
		if s == id {
			return true
		}
	}
	return false
}

// MarkSeen adds id to the seen set (idempotent, kept sorted for a stable file).
func (l *LastRun) MarkSeen(id string) {
	if l == nil || id == "" || l.Seen(id) {
		return
	}
	l.NoticesSeen = append(l.NoticesSeen, id)
	sort.Strings(l.NoticesSeen)
}

// LoadLastRun reads the run record at path. A MISSING file returns (nil, nil):
// absence is meaningful — it distinguishes "this machine has run a version that
// predates the record" from "this machine has a record". A corrupt file is
// reported as an error; callers degrade to skipping the notice rather than
// failing the command.
func LoadLastRun(path string) (*LastRun, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var l LastRun
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &l, nil
}

// SaveLastRun writes the run record atomically. Callers MUST treat a failure as
// non-fatal — a read-only home or a full disk must not fail the user's command
// over a UX marker.
func SaveLastRun(path string, l *LastRun) error {
	if l == nil {
		return fmt.Errorf("save nil last-run record")
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal last-run: %w", err)
	}
	return iox.AtomicWrite(path, append(data, '\n'), 0o644)
}
