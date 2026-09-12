package source

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/untrusted"
)

// SubagentsDir is the canonical directory name for subagents, relative to an
// agentsync home (user: ~/.agentsync/subagents/, project: <root>/.agentsync/
// subagents/). It is also the first segment of every subagent op's SourceID, so
// reconcile's dest→source write-back resolves against the same directory the
// loader read from.
//
// It is deliberately NOT the harness-side word "agents": ~/.agentsync/ carries
// BOTH the harness registry ([agents] in agentsync.toml) and the subagent
// files, and one directory apart the same word named two different types. Every
// surveyed harness spells its own *destination* directory `agents/` (see
// docs/capability-matrix.md), so the canonical↔native mapping is deliberately
// asymmetric: subagents/reviewer.md ↔ ~/.claude/agents/reviewer.md.
const SubagentsDir = "subagents"

// LegacySubagentsDir is the pre-rename spelling. A non-empty directory by this
// name in an agentsync home is a "migration pending" condition, not a source of
// subagents: nothing reads it, and Load refuses rather than silently reporting
// zero subagents (which would make the next apply disown every rendered
// subagent recorded in state).
const LegacySubagentsDir = "agents"

// SubagentSourceID returns the canonical-relative path of a subagent's source
// file — the value adapters stamp into FileOp.SourceID.
func SubagentSourceID(name string) string {
	return filepath.Join(SubagentsDir, name+".md")
}

// LegacySubagentDirError reports that <Home>/agents/ still holds subagent files
// that must be moved to <Home>/subagents/ before the tree can be loaded.
//
// It is a distinguishable error on purpose: `doctor` reports it as a failing
// check (via errors.As) instead of dying at load, and the CLI offers the guided
// one-shot move when the session is interactive.
type LegacySubagentDirError struct {
	// Home is the agentsync home holding the legacy directory.
	Home string
	// Files are the base names found under <Home>/agents/, sorted.
	Files []string
}

func (e *LegacySubagentDirError) Error() string {
	// Name the command that migrates THIS tree. The bare `agentsync migrate
	// subagents` targets the USER tree, so handing it to someone whose project
	// tree is unmigrated sends them to a command that reports "nothing to
	// migrate" and leaves them exactly where they were. The home's parent is the
	// project root when this is a project tree, and package source cannot tell
	// the two apart (both end in .agentsync), so name both forms rather than
	// guess.
	fix := fmt.Sprintf("Run `agentsync migrate subagents` to move them "+
		"(for a project tree: `agentsync migrate subagents --project %s`)", filepath.Dir(e.Home))
	return fmt.Sprintf(
		"%s still exists and holds %d subagent file(s) (%s); agentsync now reads subagents from %s. "+
			"Nothing loads the old directory, so proceeding would report zero subagents and let the next apply "+
			"delete every subagent already rendered to your agents. %s",
		filepath.Join(e.Home, LegacySubagentsDir), len(e.Files),
		// Files are directory basenames from a tree that is routinely a CLONED
		// dotfiles repo, so they are untrusted input on their way to a terminal:
		// an embedded ESC or bidi rune would otherwise reach stderr raw through
		// main's error printer. The interactive prompt for this same condition
		// already sanitizes; this is the non-interactive path.
		untrusted.Sanitize(strings.Join(e.Files, ", ")),
		filepath.Join(e.Home, SubagentsDir), fix,
	)
}

// IsLegacySubagentDir reports whether err is (or wraps) a pending
// subagent-directory migration.
func IsLegacySubagentDir(err error) bool {
	var target *LegacySubagentDirError
	return errors.As(err, &target)
}

// LegacySubagentFiles lists the *.md base names under <home>/agents/, sorted. A
// missing directory (or one holding no markdown) returns nil — the migrated
// steady state. Non-markdown entries and subdirectories are ignored for exactly
// the reason loadSubagents ignores them: they were never subagents.
func LegacySubagentFiles(fs afero.Fs, home string) ([]string, error) {
	dir := filepath.Join(home, LegacySubagentsDir)
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// CheckSubagentLayout returns a *LegacySubagentDirError when home still carries
// a non-empty legacy agents/ directory. It is the gate Load applies to every
// caller; only diagnostics that must REPORT the condition (doctor) bypass it,
// via LoadTolerant.
func CheckSubagentLayout(fs afero.Fs, home string) error {
	files, err := LegacySubagentFiles(fs, home)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	return &LegacySubagentDirError{Home: home, Files: files}
}

// MigrateSubagentTree moves <srcHome>/agents/*.md to <srcHome>/subagents/ and
// returns the moved base names, in the sorted order LegacySubagentFiles lists
// them. It is the ON-DISK half of the agents/ → subagents/ migration only.
//
// The other half — rewriting the `agents/<name>.md` SourceID spelling in the
// central state file for the entries belonging to THIS tree — stays with the
// CLI (rewriteSubagentStateIDs in internal/cli/migrate.go), which runs it right
// after this returns a non-empty list, inside the same lock hold. It does not
// live here because internal/source does not know about .state/targets.json,
// and reaching internal/state from the canonical model would be a new layering
// edge. The two halves are still one logical step: the files move first, then
// the rewrite bridges the recorded entries so the next apply matches them
// against the same dest paths instead of treating them as orphans. That
// rewrite is a bridge, not a correctness requirement — RecordOpsState
// overwrites SourceID on every apply, so an entry it misses (a teammate
// migrated the committed project tree and you pulled it, so your local state
// never saw a legacy dir) self-heals on the next apply, which is also why the
// caller skips it when this returns an error after a partial move.
func MigrateSubagentTree(srcHome string) ([]string, error) {
	// The OS filesystem, not an injected afero.Fs: the mutation half below is
	// os.Rename/os.Remove, so accepting a MemMapFs here would let a caller
	// believe it was migrating a virtual tree while real files moved.
	fs := afero.NewOsFs()
	names, err := LegacySubagentFiles(fs, srcHome)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}

	legacyDir := filepath.Join(srcHome, LegacySubagentsDir)
	newDir := filepath.Join(srcHome, SubagentsDir)

	// Both-dirs conflict policy: a hand-started migration is fine as long as no
	// filename collides. On any collision, refuse with the colliding names —
	// never overwrite, never guess which copy the user meant to keep.
	var collisions []string
	for _, name := range names {
		if _, statErr := fs.Stat(filepath.Join(newDir, name)); statErr == nil {
			collisions = append(collisions, name)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("stat %s: %w", filepath.Join(newDir, name), statErr)
		}
	}
	if len(collisions) > 0 {
		return nil, fmt.Errorf(
			"refusing to migrate: %d file(s) exist under BOTH %s and %s (%s). "+
				"Nothing was moved. Reconcile the duplicates by hand (keep one copy of each), then re-run",
			// Sanitize: these are basenames off disk in what is routinely a
			// cloned dotfiles repo, and this error goes straight to a terminal.
			// untrusted.Sanitize, not ui.Sanitize — the latter is a one-line
			// delegation to it and internal/source sits below internal/ui.
			len(collisions), legacyDir, newDir, untrusted.Sanitize(strings.Join(collisions, ", ")),
		)
	}

	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", newDir, err)
	}
	// The loop is NOT atomic, and deliberately so: an all-or-nothing move would
	// mean unwinding renames that already succeeded, which is a second failure
	// path over the same filesystem that just failed. A partial move is instead
	// made SAFE to leave — every moved file is already in its final location,
	// the collision check above only ever sees what still remains in agents/,
	// and re-running therefore picks up exactly the remainder. The state rewrite
	// is skipped on this path, which self-heals on the next apply
	// (RecordOpsState overwrites SourceID unconditionally).
	//
	// Say all of that in the error. The collision branch above states "Nothing
	// was moved" precisely because a user who reads a bare move failure has to
	// assume the worst and reconcile two directories by hand.
	for i, name := range names {
		from := filepath.Join(legacyDir, name)
		to := filepath.Join(newDir, name)
		if err := os.Rename(from, to); err != nil { //nolint:forbidigo // moves a canonical subagent file inside an agentsync home, not a native destination
			return nil, fmt.Errorf(
				"move %s → %s: %w. %d of %d file(s) were already moved and are correctly placed in %s; "+
					"the rest remain in %s. Nothing was lost or overwritten — fix the cause and re-run "+
					"`agentsync migrate subagents`, which resumes with the files still left behind",
				from, to, err, i, len(names), newDir, legacyDir,
			)
		}
	}
	// Drop the legacy directory once the move emptied it. A failure here is
	// deliberately NOT an error: os.Remove refuses a non-empty directory, and a
	// user's stray README or nested dir is theirs to keep. Leaving the directory
	// behind is harmless — the gate only ever fires on *.md files, which are all
	// gone by now.
	_ = os.Remove(legacyDir) //nolint:forbidigo // removes the emptied canonical agents/ dir under an agentsync home, not a native destination

	// names is already sorted: LegacySubagentFiles sorts what it lists, and the
	// loop above moved them in that order.
	return names, nil
}

// MigratedSourceID rewrites a legacy `agents/<name>.md` SourceID to the
// `subagents/` spelling. ok is false for every other SourceID (mcp/*, skills/*,
// the "(multiple)" sentinels, an already-migrated id), which is left untouched.
func MigratedSourceID(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	slash := filepath.ToSlash(id)
	rest, found := strings.CutPrefix(slash, LegacySubagentsDir+"/")
	if !found || rest == "" {
		return "", false
	}
	return filepath.Join(SubagentsDir, filepath.FromSlash(rest)), true
}
