package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

func newApplyCmd() *cobra.Command {
	var (
		dryRun      bool
		scopeFlag   string
		projectFlag string
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "render canonical config and write per agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			// Dry-run is read-only — it touches neither destinations nor
			// state. Acquiring the global lock would needlessly block
			// concurrent `status` / `diff` / other dry-runs behind a long
			// real apply.
			if dryRun {
				return applyRun(cmd, home, dryRun, scopeFlag, projectFlag)
			}
			return withGlobalLock(home, func() error {
				return applyRun(cmd, home, dryRun, scopeFlag, projectFlag)
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compute plan without writing destinations")
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: auto-detect from cwd)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project)")
	return cmd
}

// applyRun is the lock-protected body of the apply command. It is split
// out from newApplyCmd so the lock acquisition lives in one obvious place.
func applyRun(cmd *cobra.Command, home string, dryRun bool, scopeFlag, projectFlag string) error {
	c, sc, projectRoot, err := loadProjectedForScope(afero.NewOsFs(), home, scopeFlag, projectFlag, false)
	if err != nil {
		return err
	}

	// Announce the effective scope. Scope is auto-detected by walking up from
	// cwd to find a project marker, so without this a `apply` run from inside
	// a project (or any ancestor with a marker) could silently write to a
	// project tree the user didn't expect. The dry-run lists paths; the real
	// apply otherwise printed only an op count.
	if sc == adapter.ScopeProject {
		fmt.Fprintf(cmd.ErrOrStderr(), "scope: project (%s)\n", projectRoot)
	} else {
		fmt.Fprintln(cmd.ErrOrStderr(), "scope: user")
	}

	// Resolve ${secret:...} and ${env:...} references before rendering. The
	// result is a secrets.Resolved — the only thing adapters render from — and
	// c is left templated for the report / agent enumeration below.
	userHome := paths.HomeDir(paths.OSEnv{})
	secBackend := secrets.SelectBackend(c.Config.Secrets, home, userHome)
	envBackend := secrets.EnvBackend{}
	resolved, err := secrets.SubstituteCanonical(c, secBackend, envBackend)
	if err != nil {
		return err
	}

	agents := []string{}
	for name, ag := range c.Config.Agents {
		if ag.Enabled {
			agents = append(agents, name)
		}
	}
	if len(agents) == 0 {
		// Without this hint, `apply` prints "applied: 0 ops" and a
		// new user assumes their config "worked". Tell them how to
		// register an agent. Under project scope the likely cause is a
		// marker `agents` allowlist that intersects to nothing, so point
		// there instead of at `agent add`.
		if sc == adapter.ScopeProject {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"agentsync: no agents are enabled after applying the project marker at %s; nothing to apply.\n"+
					"  Check the [agents] allowlist in that project's .agentsync.toml.\n", projectRoot)
		} else {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"agentsync: no agents are enabled in agentsync.toml; nothing to apply.\n"+
					"  Run `agentsync agent add claude` (or opencode) to register an agent.")
		}
		return nil
	}

	reg := registryFactory()

	// Load state (needed for OwnedKeys injection in Plan).
	statePath := filepath.Join(home, ".state", "targets.json")
	s, err := state.Load(statePath)
	if err != nil {
		return err
	}

	if dryRun {
		plan, err := render.Plan(resolved, reg, agents, sc, projectRoot, s, userHome)
		if err != nil {
			return err
		}
		w := cmd.OutOrStdout()
		fmt.Fprintf(w, "Plan: %d ops total across %d agent(s)\n", plan.Total(), len(plan.PerAgent))
		for _, name := range reg.Names() {
			res, ok := plan.PerAgent[name]
			if !ok {
				continue
			}
			fmt.Fprintf(w, "  %-10s %d ops, %d skips\n", name, len(res.Ops), len(res.Skips))
			// List every destination path so the user can see exactly
			// what apply will touch. Without this the dry-run hides the
			// most useful piece of information (which files will be
			// written) behind an op count.
			for _, op := range res.Ops {
				if op.Action == "" || op.Action == "write" {
					fmt.Fprintf(w, "    write %s\n", op.Path)
				} else {
					fmt.Fprintf(w, "    %-5s %s\n", op.Action, op.Path)
				}
			}
		}
		// Foreign-collision preview: which destinations contain content
		// that agentsync does not own and will therefore be backed up
		// before overwrite. The dry-run previously hid this; users only
		// found out which files were about to be backed up after the
		// real apply ran.
		previews, perr := render.PreviewCollisions(plan, reg, s, home, userHome, sc, projectRoot)
		if perr != nil {
			return perr
		}
		if len(previews) > 0 {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "Foreign collisions: %d (the real apply will back these up before overwriting)\n", len(previews))
			for _, r := range previews {
				fmt.Fprintf(w, "  %s\n", r.String())
			}
		}
		report := render.BuildReport(c, plan, agents)
		if len(report.Rows) > 0 {
			fmt.Fprintln(w)
			report.PrintText(w)
		}
		return nil
	}

	// Real apply: render + write. The writer constructed inside
	// render.Apply enforces the foreign-collision backup invariant on
	// every destination write — there is no separate guard pass.
	plan, err := render.Plan(resolved, reg, agents, sc, projectRoot, s, userHome)
	if err != nil {
		return err
	}
	collisions, written, unchanged, applyErr := render.Apply(plan, reg, s, home, userHome, sc, projectRoot)
	if len(collisions) > 0 {
		ew := cmd.ErrOrStderr()
		fmt.Fprintf(ew, "agentsync: backed up %d pre-existing target(s) before overwriting:\n", len(collisions))
		for _, r := range collisions {
			fmt.Fprintf(ew, "  %s\n", r.String())
		}
	}

	// CRITICAL: when render.Apply errors mid-pipeline (write #5 of 10
	// failed with ENOSPC, EACCES, USB unplugged, …), files 1-4 are
	// already on disk but state.Save below has not yet run. Without
	// this best-effort state-save BEFORE returning the error, those
	// completed files would be foreign on the next apply and trigger
	// pointless backup-and-overwrite. We save whatever state we can
	// derive from the on-disk reality and surface the original error.
	//
	// Some adapter ops may have completed and others not. Recording
	// hashes from files that DO exist is always safe (RecordOpsState
	// re-reads each file); recording from files that don't exist
	// returns an error and we just skip those.
	if applyErr != nil {
		_ = saveBestEffortState(s, statePath, plan, userHome, sc, projectRoot, written)
		return applyErr
	}

	// Drop state entries for files/keys this agent no longer
	// produces. Without this, a removed MCP server / skill / hook
	// shows up as `Orphan` in `status` forever and targets.json
	// grows unbounded.
	for name, res := range plan.PerAgent {
		render.PruneStaleState(s, userHome, name, sc, projectRoot, res.Ops)
	}
	// Update state with post-apply hashes.
	for name, res := range plan.PerAgent {
		if err := render.RecordOpsState(s, userHome, name, sc, projectRoot, res.Ops); err != nil {
			return err
		}
	}
	if err := state.Save(statePath, s); err != nil {
		return err
	}
	// Bound backup growth (each is a verbatim, possibly-secret-bearing copy
	// of a pre-existing native file). Best-effort; never fails the apply.
	_ = render.PruneBackups(home, render.DefaultBackupKeep)

	w := cmd.OutOrStdout()
	// Report a clean no-op distinctly from real work: when every destination
	// path already held our exact bytes (write skipped, no mtime churn), say so
	// instead of the misleading "applied: N ops".
	if len(written) > 0 && len(unchanged) == len(written) {
		fmt.Fprintf(w, "up to date: %d ops, no changes\n", plan.Total())
	} else {
		fmt.Fprintln(w, "applied:", plan.Total(), "ops")
	}
	report := render.BuildReport(c, plan, agents)
	if len(report.Rows) > 0 {
		fmt.Fprintln(w)
		report.PrintText(w)
	}
	return nil
}

// saveBestEffortState records hashes for the ops agentsync actually wrote
// this run (the `wrote` set returned by render.Apply). Called on the apply
// error path so a partial write doesn't leave the next apply reclassifying
// those files as foreign-collisions and re-backing-them-up.
//
// It MUST key off what was written, not os.Stat existence: render.Apply stops
// at the first failing op, so a later op that was never attempted may sit on
// top of a pre-existing FOREIGN file. Recording that file as owned would
// suppress its foreign-collision backup on the next apply and silently lose
// the user's data — the exact opposite of this rescue's purpose. os.Stat
// can't distinguish "we wrote this" from "this was already here"; `wrote` can.
//
// Failures are swallowed — we already have the real error to surface and want
// to maximise the chance of the rescue state.Save landing.
func saveBestEffortState(s *state.Targets, statePath string, plan render.RenderPlan, userHome string, sc adapter.Scope, projectRoot string, wrote map[string]bool) error {
	for name, res := range plan.PerAgent {
		var done []adapter.FileOp
		for _, op := range res.Ops {
			if wrote[op.Path] {
				done = append(done, op)
			}
		}
		if len(done) == 0 {
			continue
		}
		// PruneStaleState is intentionally skipped here — we only want
		// to ADD hashes, not remove entries that may refer to the now-
		// half-applied state.
		if err := render.RecordOpsState(s, userHome, name, sc, projectRoot, done); err != nil {
			continue
		}
	}
	return state.Save(statePath, s)
}

// loadProjectedForScope loads the canonical model with plugin projection AND
// the active project overlay applied, returning the merged canonical plus the
// resolved scope and project root. Every project-scope-aware command (apply,
// status, diff, reconcile, update re-apply) goes through it so they project,
// disable, and merge identically.
//
// The marker is discovered BEFORE projection on purpose. project.Merge can only
// drop a marker-disabled plugin's c.Plugins record — the components projection
// already appended to the flat slices would still render. So the disable has to
// gate projection: marker.Plugins.Disabled is passed to LoadProjectedExcluding,
// keyed on the same plugin id Merge filters on, and Merge still runs afterward
// to drop the record (keeping report/explain listings honest).
// lenient selects the read-only/diagnostic projection: a strict same-name
// plugin.json/entry conflict is resolved entry-wins with a warning rather than a
// hard error, so status/diff still show state. Mutating callers pass false so a
// conflict aborts before any write.
func loadProjectedForScope(fs afero.Fs, home, scopeFlag, projectFlag string, lenient bool) (source.Canonical, adapter.Scope, string, error) {
	sc, projectRoot, err := resolveProjectScope(scopeFlag, projectFlag, source.Canonical{})
	if err != nil {
		return source.Canonical{}, sc, projectRoot, err
	}
	var marker *project.Marker
	if sc == adapter.ScopeProject && projectRoot != "" {
		marker, err = project.Discover(projectRoot)
		if err != nil {
			return source.Canonical{}, sc, projectRoot, fmt.Errorf("load project marker: %w", err)
		}
	}
	var disabled []string
	if marker != nil {
		disabled = marker.Plugins.Disabled
	}
	pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
	load := marketplace.LoadProjectedExcluding
	if lenient {
		load = marketplace.LoadProjectedLenient
	}
	c, err := load(fs, home, pluginCacheRoot, disabled)
	if err != nil {
		return source.Canonical{}, sc, projectRoot, err
	}
	if marker != nil {
		c = project.Merge(c, marker)
	}
	return c, sc, projectRoot, nil
}

// resolveProjectScope determines the effective scope and project root.
// Priority: --project flag > --scope flag > cwd walk-up auto-detect.
func resolveProjectScope(scopeFlag, projectFlag string, _ source.Canonical) (adapter.Scope, string, error) {
	// Explicit --project always implies project scope, so an explicit
	// --scope user alongside it is contradictory — refuse rather than
	// silently honor --project and ignore the user's --scope.
	if projectFlag != "" {
		if scopeFlag == "user" {
			return adapter.ScopeUser, "", fmt.Errorf("--scope user conflicts with --project (which implies project scope); pass only one")
		}
		abs, err := filepath.Abs(projectFlag)
		if err != nil {
			return adapter.ScopeUser, "", fmt.Errorf("resolve --project path: %w", err)
		}
		return adapter.ScopeProject, abs, nil
	}

	// --scope project without --project: walk up from cwd to find marker.
	if scopeFlag == "project" {
		cwd, err := os.Getwd()
		if err != nil {
			return adapter.ScopeUser, "", fmt.Errorf("getwd: %w", err)
		}
		marker, err := project.Discover(cwd)
		if err != nil {
			return adapter.ScopeUser, "", fmt.Errorf("discover project marker: %w", err)
		}
		if marker != nil {
			return adapter.ScopeProject, marker.Root, nil
		}
		// No marker found; fall through to user scope.
		return adapter.ScopeUser, "", nil
	}

	// Default / --scope user: auto-detect from cwd.
	if scopeFlag == "" || scopeFlag == "user" {
		// Auto-detect: if cwd has a marker, default to project scope.
		if scopeFlag == "" {
			cwd, err := os.Getwd()
			if err == nil {
				marker, merr := project.Discover(cwd)
				if merr == nil && marker != nil {
					return adapter.ScopeProject, marker.Root, nil
				}
			}
		}
		return adapter.ScopeUser, "", nil
	}

	return adapter.ScopeUser, "", fmt.Errorf("unknown --scope value %q; want user or project", scopeFlag)
}
