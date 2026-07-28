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

// ErrCorruptLastRun reports a run record that exists but cannot be parsed.
//
// It is distinguished from an I/O error on purpose. A record is a UX marker with
// no authority: an unparseable one carries no information, so the honest reading
// is "this machine has shown nothing", NOT "say nothing forever". Treating a
// parse failure like an I/O failure meant a single truncated file — the classic
// crash-mid-write artifact, or an empty file after a full disk — suppressed the
// notice permanently, silently contradicting the documented contract that a
// corrupt record only means the notice shows again later.
//
// LoadLastRun therefore returns a USABLE zero record alongside this error, so a
// caller that wants the forgiving behavior can take it and overwrite the file,
// while one that wants to bail can still check the error.
var ErrCorruptLastRun = errors.New("corrupt last-run record")

// LoadLastRun reads the run record at path.
//
// It always returns a non-nil record, so callers never nil-check before calling
// Seen/MarkSeen. A MISSING file yields the zero record with a nil error —
// absence is not an error condition, and "has this machine seen notice X?" is
// correctly "no" for a machine with no record at all. (Distinguishing a fresh
// install from an upgrade is NOT this function's job: that is the existence of
// the agentsync home, checked by the caller.)
//
// A record that exists but does not parse yields the zero record wrapped with
// ErrCorruptLastRun. Any other failure (a real I/O error — .state is a regular
// file, permissions, a bad mount) returns the zero record and that error;
// callers skip the notice rather than fail the command.
func LoadLastRun(path string) (*LastRun, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &LastRun{}, nil
		}
		return &LastRun{}, fmt.Errorf("read %s: %w", path, err)
	}
	var l LastRun
	if err := json.Unmarshal(data, &l); err != nil {
		return &LastRun{}, fmt.Errorf("%w at %s: %w", ErrCorruptLastRun, path, err)
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
