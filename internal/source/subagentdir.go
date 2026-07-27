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
	return fmt.Sprintf(
		"%s still exists and holds %d subagent file(s) (%s); agentsync now reads subagents from %s. "+
			"Nothing loads the old directory, so proceeding would report zero subagents and let the next apply "+
			"delete every subagent already rendered to your agents. Run `agentsync migrate subagents` to move them",
		filepath.Join(e.Home, LegacySubagentsDir), len(e.Files),
		// Files are directory basenames from a tree that is routinely a CLONED
		// dotfiles repo, so they are untrusted input on their way to a terminal:
		// an embedded ESC or bidi rune would otherwise reach stderr raw through
		// main's error printer. The interactive prompt for this same condition
		// already sanitizes; this is the non-interactive path.
		untrusted.Sanitize(strings.Join(e.Files, ", ")),
		filepath.Join(e.Home, SubagentsDir),
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
