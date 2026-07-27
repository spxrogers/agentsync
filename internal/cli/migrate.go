package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	// Callers on the OTHER side — apply (non-dry-run), import, reconcile — reach
	// this while already holding the lock. That is fine because withGlobalLock is
	// reentrant within a process; it was NOT fine before that, and accepting the
	// migration offer from those three blocked for the full lock timeout and then
	// failed blaming a nonexistent second process. See withGlobalLock's doc and
	// TestEnsureSubagentLayout_AcceptUnderLockHoldingCommand.
	var moved []string
	if err := withGlobalLock(userAgentsyncHome, func() error {
		var merr error
		moved, merr = migrateSubagentTree(userAgentsyncHome, srcHome, sc, projectRoot)
		return merr
	}); err != nil {
		return err
	}
	legacyDir := filepath.Join(srcHome, source.LegacySubagentsDir)
	newDir := filepath.Join(srcHome, source.SubagentsDir)
	if len(moved) == 0 {
		fmt.Fprintf(p.Out, "%s %s\n", p.Green(ui.GlyphOK),
			fmt.Sprintf("nothing to migrate — %s holds no subagent files", legacyDir))
		return nil
	}
	fmt.Fprintf(p.Out, "%s %s\n", p.Green(ui.GlyphOK),
		fmt.Sprintf("moved %d subagent file(s) from %s to %s", len(moved), legacyDir, newDir))
	for _, name := range moved {
		fmt.Fprintf(p.Out, "  %s %s\n", p.Faint(ui.GlyphArrow), ui.Sanitize(name))
	}
	if sc != adapter.ScopeProject {
		fmt.Fprintf(p.Out, "\n%s\n", p.Faint(
			"note: ~/.agentsync is often a committed dotfiles repo — commit the rename so other machines pick it up.",
		))
	}
	return nil
}

// migrateSubagentTree moves <srcHome>/agents/*.md to <srcHome>/subagents/ and
// rewrites the SourceID spelling in the state entries belonging to THIS tree.
// It returns the moved base names, sorted.
//
// The two halves are one logical step: the files move first, then the state
// rewrite bridges the recorded entries so the next apply matches them against
// the same dest paths instead of treating them as orphans. The rewrite is a
// bridge, not a correctness requirement — RecordOpsState overwrites SourceID on
// every apply, so an entry this misses (a teammate migrated the committed
// project tree and you pulled it, so your local state never saw a legacy dir)
// self-heals on the next apply.
//
// The state rewrite is SCOPED to the tree being migrated: state is central and
// holds keys for every project, and other project trees migrate on their own
// schedule.
func migrateSubagentTree(userAgentsyncHome, srcHome string, sc adapter.Scope, projectRoot string) ([]string, error) {
	fs := afero.NewOsFs()
	names, err := source.LegacySubagentFiles(fs, srcHome)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}

	legacyDir := filepath.Join(srcHome, source.LegacySubagentsDir)
	newDir := filepath.Join(srcHome, source.SubagentsDir)

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
			len(collisions), legacyDir, newDir, ui.Sanitize(strings.Join(collisions, ", ")),
		)
	}

	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", newDir, err)
	}
	for _, name := range names {
		from := filepath.Join(legacyDir, name)
		to := filepath.Join(newDir, name)
		if err := os.Rename(from, to); err != nil { //nolint:forbidigo // moves a canonical subagent file inside an agentsync home, not a native destination
			return nil, fmt.Errorf("move %s → %s: %w", from, to, err)
		}
	}
	// Drop the legacy directory once the move emptied it. A failure here is
	// deliberately NOT an error: os.Remove refuses a non-empty directory, and a
	// user's stray README or nested dir is theirs to keep. Leaving the directory
	// behind is harmless — the gate only ever fires on *.md files, which are all
	// gone by now.
	_ = os.Remove(legacyDir) //nolint:forbidigo // removes the emptied canonical agents/ dir under an agentsync home, not a native destination

	if err := rewriteSubagentStateIDs(userAgentsyncHome, sc, projectRoot); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
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
	for key, entry := range st.Files {
		if !stateKeyInTree(key, sc, portableProject) {
			continue
		}
		if id, ok := migratedSourceID(entry.SourceID); ok {
			entry.SourceID = id
			st.Files[key] = entry
			changed = true
		}
	}
	for key, entry := range st.Keys {
		if !stateKeyInTree(key, sc, portableProject) {
			continue
		}
		if id, ok := migratedSourceID(entry.SourceID); ok {
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

// stateKeyInTree reports whether a state key ("<agent>:<scope>:<project>:…")
// belongs to the tree identified by scope + portable project root. Both the
// project root and the dest path may themselves contain ':' (a Windows drive
// letter), so only the first two fields are split off positionally and the
// project root is matched as a prefix of the remainder.
func stateKeyInTree(key string, sc adapter.Scope, portableProject string) bool {
	parts := strings.SplitN(key, ":", 3)
	if len(parts) < 3 {
		return false
	}
	if parts[1] != sc.String() {
		return false
	}
	return strings.HasPrefix(parts[2], portableProject+":")
}

// migratedSourceID rewrites a legacy `agents/<name>.md` SourceID to the
// `subagents/` spelling. ok is false for every other SourceID (mcp/*, skills/*,
// the "(multiple)" sentinels, an already-migrated id), which is left untouched.
func migratedSourceID(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	slash := filepath.ToSlash(id)
	rest, found := strings.CutPrefix(slash, source.LegacySubagentsDir+"/")
	if !found || rest == "" {
		return "", false
	}
	return filepath.Join(source.SubagentsDir, filepath.FromSlash(rest)), true
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
	fmt.Fprintf(w, "%s agentsync now reads subagents from %s, not %s.\n",
		ui.GlyphInfo, filepath.Join(pending.Home, source.SubagentsDir), filepath.Join(pending.Home, source.LegacySubagentsDir))
	fmt.Fprintf(w, "  %d file(s) are still in the old directory: %s\n", len(pending.Files), ui.Sanitize(strings.Join(pending.Files, ", ")))
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
