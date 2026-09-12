package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
)

// newMigrateCmd is the one-shot maintenance group for retired on-disk layouts.
// It exists so the "migration pending" error every source-loading command
// raises can name ONE exact command to run — including under --no-input, where
// the guided offer is unavailable. It is transitional by design: when the
// legacy spelling is no longer plausible in the wild, the group goes away.
func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "one-shot migrations for retired canonical layouts",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newMigrateSubagentsCmd())
	strictGroup(cmd)
	markScopeAware(cmd) // `migrate subagents` migrates one tree, chosen by scope
	return cmd
}

func newMigrateSubagentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subagents",
		Short: "move the canonical agents/ directory to subagents/",
		Long: `subagents moves <home>/agents/*.md to <home>/subagents/*.md and rewrites the
recorded state entries for that tree so the next apply recognizes the files it
already wrote.

agentsync's canonical tree used to spell the subagent directory ` + "`agents/`" + `, one
directory away from the ` + "`[agents]`" + ` harness registry in agentsync.toml — the same
word naming two different types. The subagent side moved; the harness side did
not (` + "`[agents]`" + `, ` + "`--agents`" + ` and the ` + "`agent`" + ` command group are unchanged).

The move is refused, with the colliding names listed, if a file of the same name
already exists under subagents/ — nothing is ever overwritten. Run it once per
tree: user scope by default, or --scope project / --project <path> for a
project's committed .agentsync/ tree.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			// Scope resolution can PROMPT, so it stays outside the lock;
			// runSubagentMigration takes the lock around the mutation itself.
			sc, projectRoot, err := resolveScope(cmd, noInputFlag(cmd))
			if err != nil {
				return err
			}
			p, perr := newPrinter(cmd)
			if perr != nil {
				return perr
			}
			return runSubagentMigration(p, home, sc, projectRoot)
		},
	}
	markScopeAware(cmd)
	return cmd
}

// sourceHomeForScope returns the agentsync home the canonical source is read
// from for a resolved scope: the user home, or the project's .agentsync/ tree.
func sourceHomeForScope(userAgentsyncHome string, sc adapter.Scope, projectRoot string) string {
	if sc == adapter.ScopeProject && projectRoot != "" {
		return project.Home(projectRoot)
	}
	return userAgentsyncHome
}

// runSubagentMigration performs the guided move for ONE tree and reports what
// it did. It is the single implementation behind both `agentsync migrate
// subagents` and the interactive offer, so the two can never drift.
func runSubagentMigration(p *ui.Printer, userAgentsyncHome string, sc adapter.Scope, projectRoot string) error {
	srcHome := sourceHomeForScope(userAgentsyncHome, sc, projectRoot)
	// The lock lives HERE, not at the command, because `migrate subagents` is
	// not the only caller: ensureSubagentLayout offers the same move from
	// apply/import — including `apply --dry-run`, which is deliberately
	// lock-free because it "touches neither destinations nor state", and from
	// the read-only status/diff/explain paths. Those callers still perform the
	// real mutation (an os.Rename loop plus a read-modify-write of
	// .state/targets.json), and lock.go requires the lock around exactly that.
	// Taking it here covers every caller by construction.
	//
	// Callers on the OTHER side reach this while ALREADY holding the lock —
	// apply (non-dry-run), import, reconcile, and `plugin upgrade <id>` without
	// --lossless (its RunE is lockedRun, and the fail-closed source.Load runs
	// only under --lossless, so the offer is reachable under the lock). That is
	// fine because withGlobalLock is reentrant within a process; it was NOT fine
	// before that, and accepting the offer from any of them blocked for the full
	// lock timeout and then failed blaming a nonexistent second process.
	//
	// Do not treat this list as exhaustive — that is the point of making the
	// primitive reentrant rather than auditing callers. Two attempts at
	// enumerating them were wrong in opposite directions. See withGlobalLock's
	// doc and TestEnsureSubagentLayout_AcceptUnderLockHoldingCommand.
	var moved []string
	if err := withGlobalLock(userAgentsyncHome, func() error {
		// The move and the state rewrite are one logical step and must stay
		// inside one lock hold: the rewrite bridges the recorded entries for the
		// files that just moved. source.MigrateSubagentTree owns the on-disk
		// half (it owns the layout the rename is between); the state half stays
		// here, where the scope→state-key conversion lives — internal/source
		// does not, and should not, know about .state/targets.json.
		names, merr := source.MigrateSubagentTree(srcHome)
		if merr != nil || len(names) == 0 {
			return merr
		}
		if rerr := rewriteSubagentStateIDs(userAgentsyncHome, sc, projectRoot); rerr != nil {
			return rerr
		}
		moved = names
		return nil
	}); err != nil {
		return err
	}
	legacyDir := filepath.Join(srcHome, source.LegacySubagentsDir)
	newDir := filepath.Join(srcHome, source.SubagentsDir)
	if len(moved) == 0 {
		p.Successf(ui.EmojiSuccess, "nothing to migrate — %s holds no subagent files", legacyDir)
		return nil
	}
	p.Successf(ui.EmojiSuccess, "moved %d subagent file(s) from %s to %s", len(moved), legacyDir, newDir)
	for _, name := range moved {
		fmt.Fprintf(p.Out, "  %s %s\n", p.Faint(ui.GlyphArrow), ui.Sanitize(name))
	}
	if sc != adapter.ScopeProject {
		p.Infof("~/.agentsync is often a committed dotfiles repo — commit the rename so other machines pick it up.")
	}
	return nil
}

// rewriteSubagentStateIDs rewrites `agents/<name>.md` SourceID values to
// `subagents/<name>.md` in the central state file, for the entries belonging to
// the migrated tree only (scope + project root).
func rewriteSubagentStateIDs(userAgentsyncHome string, sc adapter.Scope, projectRoot string) error {
	statePath := filepath.Join(userAgentsyncHome, ".state", "targets.json")
	st, err := state.Load(statePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	userHome := paths.HomeDir(paths.OSEnv{})
	portableProject := paths.HomeRelative(userHome, projectRoot)

	changed := false
	scopeName := sc.String()
	for key, entry := range st.Files {
		if key.Scope != scopeName || key.Project != portableProject {
			continue
		}
		if id, ok := source.MigratedSourceID(entry.SourceID); ok {
			entry.SourceID = id
			st.Files[key] = entry
			changed = true
		}
	}
	for key, entry := range st.Keys {
		if key.Scope != scopeName || key.Project != portableProject {
			continue
		}
		if id, ok := source.MigratedSourceID(entry.SourceID); ok {
			entry.SourceID = id
			st.Keys[key] = entry
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return state.Save(statePath, st)
}

// ensureSubagentLayout is the interactive half of the migration gate. Every
// source-loading command is already fail-closed through source.Load, which
// refuses an unmigrated tree with a *source.LegacySubagentDirError; this runs
// BEFORE the load in the commands users hit daily so the refusal can be an
// offer instead of a dead end.
//
// It checks the user home and, at project scope, the project tree. When a
// migration is pending and the session is interactive, it offers the move and
// performs it in place. Under --no-input (or with no TTY) it returns the
// distinguishable error, which names the exact command to run.
func ensureSubagentLayout(cmd *cobra.Command, userAgentsyncHome string, sc adapter.Scope, projectRoot string) error {
	fs := afero.NewOsFs()
	// The user tree is checked in both scopes: a project-scope apply still loads
	// the user canonical and overlays the project tree onto it, so an unmigrated
	// user tree is just as disowning there.
	trees := []struct {
		home  string
		scope adapter.Scope
		root  string
	}{{userAgentsyncHome, adapter.ScopeUser, ""}}
	if sc == adapter.ScopeProject && projectRoot != "" {
		trees = append(trees, struct {
			home  string
			scope adapter.Scope
			root  string
		}{project.Home(projectRoot), adapter.ScopeProject, projectRoot})
	}

	for _, t := range trees {
		err := source.CheckSubagentLayout(fs, t.home)
		if err == nil {
			continue
		}
		var pending *source.LegacySubagentDirError
		if !errors.As(err, &pending) {
			return err
		}
		p, perr := newPrinter(cmd)
		if perr != nil {
			return perr
		}
		if !subagentMigrationPrompter(cmd, p, pending) {
			return err
		}
		if merr := runSubagentMigration(p, userAgentsyncHome, t.scope, t.root); merr != nil {
			return merr
		}
	}
	return nil
}

// subagentMigrationPrompter is the interactive offer, injectable so tests can
// drive the accept/decline branches without a terminal.
var subagentMigrationPrompter = promptSubagentMigration

// promptSubagentMigration asks whether to perform the one-shot move. It fails
// closed (false) with no TTY or under --no-input, so headless runs get the
// error naming the command instead of blocking on a Read.
func promptSubagentMigration(cmd *cobra.Command, p *ui.Printer, pending *source.LegacySubagentDirError) bool {
	if noInputFlag(cmd) || !stdinIsTerminal(cmd) {
		return false
	}
	// The prompt goes to STDERR: it can fire before a `status --json` /
	// `diff --json` payload, and stdout is that payload's channel.
	w := cmd.ErrOrStderr()
	p.Fdiagf(w, ui.LevelInfo, "agentsync now reads subagents from %s, not %s.",
		filepath.Join(pending.Home, source.SubagentsDir), filepath.Join(pending.Home, source.LegacySubagentsDir))
	p.Fdetailf(w, "%d file(s) are still in the old directory: %s", len(pending.Files), ui.Sanitize(strings.Join(pending.Files, ", ")))
	return promptConfirmYes(w, cmd.InOrStdin(), "  Move them now (and update recorded state)? [y/N]: ")
}

// promptConfirmYes writes prompt and reads one line, returning true only for an
// explicit yes. Anything else — including EOF — is a no, so the caller falls
// through to the fail-closed error rather than acting on silence.
func promptConfirmYes(w io.Writer, in io.Reader, prompt string) bool {
	r := bufio.NewReader(in)
	fmt.Fprint(w, prompt)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
