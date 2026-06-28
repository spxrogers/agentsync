package cli

import (
	"fmt"
	"sort"

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
and the revert is itself revertible. By default it undoes the most recent apply
(restores the previous checkpoint); --to picks a specific one.

revert only moves the DESTINATION. The next 'agentsync apply' re-renders from the
canonical config and would overwrite the reverted state, so revert prints a notice
telling you to reconcile (agentsync reconcile / import) or fix the canonical source
first.

Only agentsync-managed (git-backed) user-scope dirs can be reverted; enable git
backup ([destination_directory_git_backup]) and run an apply first.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := newPrinter(cmd)
			if err != nil {
				return err
			}
			if all && len(args) > 0 {
				return fmt.Errorf("--all reverts every managed dir; do not also name an agent")
			}
			if all && toRef != "" {
				return fmt.Errorf("--to names a checkpoint in one repo and can't apply across --all; revert a single agent with --to")
			}
			if !all && len(args) == 0 {
				return fmt.Errorf("name an agent to revert (e.g. `agentsync revert claude`) or pass --all")
			}

			reg := registryFactory()
			id := revertIdentity()

			if all {
				return revertAll(p, reg, dryRun, id)
			}
			return revertOne(p, reg, args[0], toRef, dryRun, id, true)
		},
	}
	cmd.Flags().StringVar(&toRef, "to", "", "checkpoint to restore (commit hash or relative like HEAD~2); default: undo the most recent apply")
	cmd.Flags().BoolVar(&all, "all", false, "revert every agentsync-managed destination dir to its last checkpoint")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing")
	return cmd
}

// revertIdentity loads the commit identity from agentsync.toml (best-effort;
// defaults when the config is absent or unreadable).
func revertIdentity() agit.Identity {
	home := paths.AgentsyncHome(paths.OSEnv{})
	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		return agit.Identity{}
	}
	g := c.Config.DestinationGitBackup
	return agit.Identity{Name: g.AuthorName, Email: g.AuthorEmail}
}

// revertOne reverts a single agent's dir. When strict is true (an explicit agent
// arg) an unknown/unmanaged dir is an error; under --all it is a skip.
func revertOne(p *ui.Printer, reg *adapter.Registry, name, toRef string, dryRun bool, id agit.Identity, strict bool) error {
	ad := reg.Lookup(name)
	if ad == nil {
		if strict {
			return fmt.Errorf("unknown agent %q", name)
		}
		return nil
	}
	vh, ok := ad.(adapter.VersionedHome)
	if !ok {
		if strict {
			return fmt.Errorf("agent %q has no git-backed destination dir", name)
		}
		return nil
	}
	dir, ok := vh.HomeDir(adapter.ScopeUser, "")
	if !ok || dir == "" {
		if strict {
			return fmt.Errorf("agent %q has no user-scope destination dir to revert", name)
		}
		return nil
	}

	st, err := agit.Detect(dir)
	if err != nil {
		return err
	}
	if st != agit.StateAgentsyncOwned {
		if strict {
			return fmt.Errorf("%s is not an agentsync-managed git backup (state: %s); "+
				"enable [destination_directory_git_backup] and run `agentsync apply` first", dir, st)
		}
		fmt.Fprintf(p.Err, "%s %s: %s — skipping\n", p.Faint(ui.GlyphInfo), name, st)
		return nil
	}

	repo, err := agit.Open(dir)
	if err != nil {
		return err
	}

	target := toRef
	if target == "" {
		multi, herr := repo.HasMultipleCheckpoints()
		if herr != nil {
			return herr
		}
		if !multi {
			if strict {
				return fmt.Errorf("%s has only one checkpoint; nothing earlier to revert to", dir)
			}
			fmt.Fprintf(p.Err, "%s %s: only one checkpoint — skipping\n", p.Faint(ui.GlyphInfo), name)
			return nil
		}
		target = "HEAD~1"
	}

	if dryRun {
		return previewRevert(p, repo, name, dir, target)
	}

	targetHash, err := repo.Resolve(target)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("agentsync revert: %s → %s", name, shortRef(targetHash))
	h, err := repo.Restore(target, msg, id)
	if err != nil {
		return err
	}
	if h == "" {
		fmt.Fprintf(p.Out, "%s %s already matches %s; nothing to revert.\n", p.Faint(ui.GlyphInfo), dir, shortRef(targetHash))
		return nil
	}
	fmt.Fprintf(p.Out, "%s reverted %s to checkpoint %s\n", p.Green(ui.GlyphOK), dir, shortRef(targetHash))
	printOutOfSyncNotice(p, dir)
	return nil
}

// revertAll reverts every agentsync-managed dir to its previous checkpoint.
func revertAll(p *ui.Printer, reg *adapter.Registry, dryRun bool, id agit.Identity) error {
	names := reg.Names()
	sort.Strings(names)
	var reverted []string
	for _, name := range names {
		ad := reg.Lookup(name)
		vh, ok := ad.(adapter.VersionedHome)
		if !ok {
			continue
		}
		dir, ok := vh.HomeDir(adapter.ScopeUser, "")
		if !ok || dir == "" {
			continue
		}
		if st, _ := agit.Detect(dir); st != agit.StateAgentsyncOwned {
			continue // not ours / untracked — silently pass over under --all
		}
		if err := revertOne(p, reg, name, "", dryRun, id, false); err != nil {
			return err
		}
		reverted = append(reverted, dir)
	}
	if len(reverted) == 0 {
		fmt.Fprintf(p.Out, "%s no agentsync-managed destination dirs to revert.\n", p.Faint(ui.GlyphInfo))
	}
	return nil
}

// previewRevert prints what a revert would change, without writing.
func previewRevert(p *ui.Printer, repo *agit.Repo, name, dir, target string) error {
	targetHash, changes, err := repo.Plan(target)
	if err != nil {
		return err
	}
	subject := checkpointSubject(repo, targetHash)
	fmt.Fprintf(p.Out, "%s would revert %s (%s) to %s%s\n",
		p.Bold("dry-run:"), name, dir, p.Cyan(shortRef(targetHash)), subject)
	if len(changes) == 0 {
		fmt.Fprintf(p.Out, "  (already matches; nothing to change)\n")
		return nil
	}
	for _, c := range changes {
		fmt.Fprintf(p.Out, "    %s %-6s %s\n", p.Cyan(ui.GlyphArrow), c.Kind, c.Path)
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
