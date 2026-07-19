package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	agit "github.com/spxrogers/agentsync/internal/git"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
)

func newRevertCmd() *cobra.Command {
	var (
		toRef  string
		all    bool
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "revert [<agent>]",
		Short: "roll a destination agent dir back to a prior apply checkpoint",
		Long: `revert restores an agent's destination dir (e.g. ~/.claude) to a prior
apply checkpoint from its local-only git history, recovering from a bad apply.

It is append-only: revert records a NEW commit, so the history is never rewritten
and the revert is itself revertible. Uncommitted hand-edits to tracked files are
preserved as a snapshot commit first, so nothing is lost — that snapshot lands in the
local-only history and, like the files it versions, may contain secrets in cleartext.
Untracked files you dropped into the dir (and gitignored files) are left untouched —
revert only rewinds the files agentsync itself versions. By default it undoes the most
recent apply (restores the previous checkpoint); --to picks a specific one.

Even the FIRST apply is revertible: before that apply overwrites the dir, agentsync
records a pre-apply baseline checkpoint of its prior contents, so a revert right
after a first apply restores the genuine pre-apply state.

revert only moves the DESTINATION. The next 'agentsync apply' re-renders from the
canonical config and would overwrite the reverted state, so revert prints a notice
telling you to reconcile (agentsync reconcile / import) or fix the canonical source
first.

An agent may version more than one directory (its config dir plus a shared dir like
~/.agents/skills); revert restores each one it owns. Only agentsync-managed
(git-backed) user-scope dirs can be reverted; enable git backup
([destination_directory_git_backup]) and run an apply first.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("--all reverts every managed dir; do not also name an agent")
			}
			if all && toRef != "" {
				return fmt.Errorf("--to names a checkpoint in one repo and can't apply across --all; revert a single agent with --to")
			}
			if !all && len(args) == 0 {
				return fmt.Errorf("name an agent to revert (e.g. `agentsync revert claude`) or pass --all")
			}
			p, err := newPrinter(cmd)
			if err != nil {
				return err
			}
			home := paths.AgentsyncHome(paths.OSEnv{})
			reg := registryFactory()
			id := revertIdentity(home)

			// A revert mutates destination repos; take the same global lock apply
			// holds so a concurrent apply/revert can't interleave go-git index writes
			// on the same dir. Dry-run is read-only and skips the lock.
			run := func() error {
				if all {
					return revertAll(p, reg, dryRun, id)
				}
				return revertAgent(p, reg, args[0], toRef, dryRun, id, true)
			}
			if dryRun {
				return run()
			}
			return withGlobalLock(home, run)
		},
	}
	cmd.Flags().StringVar(&toRef, "to", "", "checkpoint to restore (commit hash or relative like HEAD~2); default: undo the most recent apply")
	cmd.Flags().BoolVar(&all, "all", false, "revert every agentsync-managed destination dir to its last checkpoint")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing")
	return cmd
}

// revertIdentity loads the commit identity from agentsync.toml (best-effort;
// defaults when the config is absent or unreadable).
func revertIdentity(home string) agit.Identity {
	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		return agit.Identity{}
	}
	g := c.Config.DestinationGitBackup
	return agit.Identity{Name: g.AuthorName, Email: g.AuthorEmail}
}

// revertAgent reverts every version root the named agent owns. When strict is true
// (an explicit agent arg) an unknown agent / no-versioned-dir is an error; under
// --all it is a skip.
func revertAgent(p *ui.Printer, reg *adapter.Registry, name, toRef string, dryRun bool, id agit.Identity, strict bool) error {
	ad := reg.Lookup(name)
	if ad == nil {
		if strict {
			return fmt.Errorf("unknown agent %q", name)
		}
		return nil
	}
	vd, ok := ad.(adapter.VersionedDirs)
	if !ok {
		if strict {
			return fmt.Errorf("agent %q has no git-backed destination dir", name)
		}
		return nil
	}
	roots := denestRoots(cleanAll(vd.VersionRoots(adapter.ScopeUser, "")))
	if len(roots) == 0 {
		if strict {
			return fmt.Errorf("agent %q has no user-scope destination dir to revert", name)
		}
		return nil
	}
	if toRef != "" && len(roots) > 1 {
		return fmt.Errorf("agent %q versions %d directories; --to is ambiguous — omit it to undo the most recent apply on each", name, len(roots))
	}
	owners := versionRootOwners(reg, reg.Names(), adapter.ScopeUser, "")
	var anyManaged bool
	for _, root := range roots {
		// ownersFor (not owners[root]): this agent's own de-nested root may be a
		// child parent-folded into an ancestor key in the global owners map, so an
		// exact-key lookup would drop the cross-agent warning (issue #154).
		managed, err := revertRoot(p, root, toRef, dryRun, id, sharedExcluding(ownersFor(owners, root), name), strict && len(roots) == 1)
		if err != nil {
			return err
		}
		anyManaged = anyManaged || managed
	}
	if !anyManaged && strict {
		return fmt.Errorf("none of %q's directories are agentsync-managed git backups; "+
			"enable [destination_directory_git_backup] and run `agentsync apply` first", name)
	}
	return nil
}

// revertAll reverts every agentsync-managed version root across all adapters.
func revertAll(p *ui.Printer, reg *adapter.Registry, dryRun bool, id agit.Identity) error {
	roots := enabledVersionRoots(reg, reg.Names(), adapter.ScopeUser, "")
	var any bool
	for _, root := range roots {
		// Under --all every owner is reverted anyway, so no cross-agent surprise.
		managed, err := revertRoot(p, root, "", dryRun, id, nil, false)
		if err != nil {
			return err
		}
		any = any || managed
	}
	if !any {
		fmt.Fprintf(p.Out, "%s no agentsync-managed destination dirs to revert.\n", p.Faint(ui.GlyphInfo))
	}
	return nil
}

// sharedExcluding returns the owners of a root other than `self` — i.e. the OTHER
// agents whose files also live in this shared dir.
func sharedExcluding(owners []string, self string) []string {
	var others []string
	for _, o := range owners {
		if o != self {
			others = append(others, o)
		}
	}
	return others
}

// revertRoot reverts a single version-root dir. Returns whether the dir is an
// agentsync-managed backup (exactly, or folded into a parent repo). strict turns an
// unmanaged dir into an error instead of a skip. sharedWith lists OTHER agents that
// also write into this dir (for the cross-agent rollback warning).
func revertRoot(p *ui.Printer, root, toRef string, dryRun bool, id agit.Identity, sharedWith []string, strict bool) (managed bool, err error) {
	// Act only on a repo whose EXACT dir is the agentsync root. Detect's upward
	// search can match a PARENT repo (a folded child like ~/.claude/skills inside
	// ~/.claude); in that case the dir is managed-by-parent — skip it, the parent's
	// revert covers it.
	exact, err := agit.OwnsExactly(root)
	if err != nil {
		return false, err
	}
	if !exact {
		st, derr := agit.Detect(root)
		if derr != nil {
			return false, derr
		}
		if st == agit.StateAgentsyncOwned {
			// Folded into a parent agentsync repo — managed, but reverted via the parent.
			fmt.Fprintf(p.Err, "%s %s is versioned as part of a parent directory; revert that to roll it back.\n",
				p.Faint(ui.GlyphInfo), root)
			return true, nil
		}
		if strict {
			return false, fmt.Errorf("%s is not an agentsync-managed git backup (state: %s); "+
				"enable [destination_directory_git_backup] and run `agentsync apply` first", root, st)
		}
		return false, nil
	}
	repo, err := agit.Open(root)
	if err != nil {
		return true, err
	}

	// A foreign git repo may have appeared below this owned root since init (e.g. the
	// user cloned one into ~/.claude/skills). agentsync's clean ownership of the root
	// no longer holds, so refuse to revert as a precaution BEFORE any write — checked
	// before the dry-run early return, so a preview reports the skip too and never
	// claims it will revert. (Restore is a surgical tracked-delta apply and would leave
	// the untracked foreign checkout in place, but reverting a root that now wraps
	// another repo is a hazard the user should resolve first — remove or reconcile the
	// inner repo, then revert.)
	nested, nerr := agit.HasNestedRepoBelow(root)
	if nerr != nil {
		return true, nerr
	}
	if nested {
		if strict {
			return true, fmt.Errorf("%s contains a nested git repository; refusing to revert "+
				"as a precaution — remove or reconcile the inner repo first", root)
		}
		fmt.Fprintf(p.Err, "%s a nested git repository now lives below %s; skipping revert "+
			"as a precaution — remove or reconcile the inner repo first.\n", p.Yellow(ui.GlyphWarn+" note:"), root)
		return true, nil
	}

	target := toRef
	if target == "" {
		multi, herr := repo.HasMultipleCheckpoints()
		if herr != nil {
			return true, herr
		}
		if !multi {
			fmt.Fprintf(p.Err, "%s %s: only one checkpoint — skipping\n", p.Faint(ui.GlyphInfo), root)
			return true, nil
		}
		target = "HEAD~1"
	}

	if dryRun {
		return true, previewRevert(p, repo, root, target)
	}

	// Resolve the target to a concrete hash for the commit message; Restore also
	// snapshots uncommitted tracked edits internally (safe-by-construction) and
	// resolves the ref itself before that snapshot moves HEAD.
	targetHash, err := repo.Resolve(target)
	if err != nil {
		return true, err
	}
	msg := fmt.Sprintf("agentsync revert: %s → %s", root, shortRef(targetHash))
	// Restore folds in SnapshotDirtyTracked and returns its hash. Do NOT announce the
	// snapshot before the revert lands: on a partial failure Restore returns a hinted
	// error naming the snapshot + pre-revert HEAD, so we print the "preserved" notice
	// only once the revert actually succeeds and the error can't contradict it.
	h, snap, err := repo.Restore(targetHash, msg, id)
	if err != nil {
		return true, err
	}
	if snap != "" {
		fmt.Fprintf(p.Out, "%s preserved uncommitted changes in %s as snapshot %s\n",
			p.Faint(ui.GlyphInfo), root, shortRef(snap))
		// The snapshot commits the dest files verbatim; like everything in this
		// local-only history they may hold secrets resolved to cleartext. Caution the
		// user rather than silently persist a freshly-typed secret (issue #126).
		fmt.Fprintf(p.Err, "%s that snapshot is kept in the local-only history and, like the files it versions, may contain secrets in cleartext.\n",
			p.Faint(ui.GlyphInfo))
	}
	if h == "" {
		fmt.Fprintf(p.Out, "%s %s already matches %s; nothing to revert.\n", p.Faint(ui.GlyphInfo), root, shortRef(targetHash))
		return true, nil
	}
	fmt.Fprintf(p.Out, "%s reverted %s to checkpoint %s\n", p.Green(ui.GlyphOK), root, shortRef(targetHash))
	if len(sharedWith) > 0 {
		fmt.Fprintf(p.Err, "%s %s is shared with %s — this also rolled back their files in it.\n",
			p.Yellow(ui.GlyphWarn+" note:"), root, strings.Join(sharedWith, ", "))
	}
	printOutOfSyncNotice(p, root)
	return true, nil
}

// previewRevert prints what a revert would change, without writing.
func previewRevert(p *ui.Printer, repo *agit.Repo, root, target string) error {
	targetHash, changes, err := repo.Plan(target)
	if err != nil {
		return err
	}
	subject := checkpointSubject(repo, targetHash)
	fmt.Fprintf(p.Out, "%s would revert %s to %s%s\n",
		p.Bold("dry-run:"), root, p.Cyan(shortRef(targetHash)), subject)
	if clean, _ := repo.IsClean(); !clean {
		fmt.Fprintf(p.Out, "  (uncommitted changes to tracked files would be snapshotted first, then preserved in history)\n")
	}
	if untracked, _ := repo.UntrackedPaths(); len(untracked) > 0 {
		fmt.Fprintf(p.Out, "  (%d untracked file(s) in this dir are left untouched)\n", len(untracked))
	}
	if len(changes) == 0 {
		fmt.Fprintf(p.Out, "  (already matches; nothing to change)\n")
		return nil
	}
	for _, c := range changes {
		// c.Path is a tracked dest path embedding a config-derived component
		// dirname; sanitize on display (issue #93/#171).
		fmt.Fprintf(p.Out, "    %s %-6s %s\n", p.Cyan(ui.GlyphArrow), c.Kind, ui.Sanitize(c.Path))
	}
	return nil
}

// checkpointSubject returns " — <subject>" for the commit, or "" if not found.
func checkpointSubject(repo *agit.Repo, hash string) string {
	cps, err := repo.Log(0)
	if err != nil {
		return ""
	}
	for _, c := range cps {
		if c.Hash == hash {
			return " — " + c.Subject
		}
	}
	return ""
}

// cleanAll returns filepath.Clean of each path (dropping empties).
func cleanAll(dirs []string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d != "" {
			out = append(out, filepath.Clean(d))
		}
	}
	return out
}

// shortRef abbreviates a full hash to 7 chars for display.
func shortRef(h string) string {
	if len(h) >= 7 {
		return h[:7]
	}
	return h
}

// printOutOfSyncNotice warns that the destination now diverges from canonical.
func printOutOfSyncNotice(p *ui.Printer, dir string) {
	fmt.Fprintf(p.Err, "%s the destination directory %s is now out of sync with the agentsync configuration.\n",
		p.Yellow(ui.GlyphWarn+" note:"), dir)
	fmt.Fprintf(p.Err, "  Reconcile as needed (`agentsync reconcile` / `agentsync import`) before your next "+
		"`agentsync apply` to avoid re-losing these changes.\n")
}
