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
	"github.com/spxrogers/agentsync/internal/ui"
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
	p, err := newPrinter(cmd)
	if err != nil {
		return err
	}
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
		fmt.Fprintf(p.Err, "%s project (%s)\n", p.Faint("scope:"), projectRoot)
	} else {
		fmt.Fprintf(p.Err, "%s user\n", p.Faint("scope:"))
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
			fmt.Fprintf(p.Err,
				"%s no agents are enabled after applying the project marker at %s; nothing to apply.\n"+
					"  Check the [agents] allowlist in that project's .agentsync.toml.\n", p.Yellow("agentsync:"), projectRoot)
		} else {
			fmt.Fprintf(p.Err,
				"%s no agents are enabled in agentsync.toml; nothing to apply.\n"+
					"  Run `agentsync agent add claude` (or opencode) to register an agent.\n", p.Yellow("agentsync:"))
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
		w := p.Out
		fmt.Fprintf(w, "%s %d ops total across %d agent(s)\n", p.Bold("Plan:"), plan.Total(), len(plan.PerAgent))
		for _, name := range reg.Names() {
			res, ok := plan.PerAgent[name]
			if !ok {
				continue
			}
			fmt.Fprintf(w, "  %s %d ops, %d skips\n", p.Bold(ui.Pad(name, 10)), len(res.Ops), len(res.Skips))
			// List every destination path so the user can see exactly
			// what apply will touch. Without this the dry-run hides the
			// most useful piece of information (which files will be
			// written) behind an op count.
			for _, op := range res.Ops {
				action := op.Action
				if action == "" {
					action = "write"
				}
				fmt.Fprintf(w, "    %s %s %s\n", p.Cyan(ui.GlyphArrow), p.Cyan(ui.Pad(action, 5)), op.Path)
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
			fmt.Fprintf(w, "%s %d (the real apply will back these up before overwriting)\n",
				p.Yellow(ui.GlyphWarn+" Foreign collisions:"), len(previews))
			for _, r := range previews {
				fmt.Fprintf(w, "  %s\n", r.String())
			}
		}
		report := render.BuildReport(c, plan, agents)
		if len(report.Rows) > 0 {
			fmt.Fprintln(w)
			report.PrintTextStyled(w, p)
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
		fmt.Fprintf(p.Err, "%s backed up %d pre-existing target(s) before overwriting:\n",
			p.Yellow("agentsync:"), len(collisions))
		for _, r := range collisions {
			fmt.Fprintf(p.Err, "  %s\n", r.String())
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

	w := p.Out
	// Report a clean no-op distinctly from real work: when every destination
	// path already held our exact bytes (write skipped, no mtime churn), say so
	// instead of the misleading "applied: N ops".
	if len(written) > 0 && len(unchanged) == len(written) {
		fmt.Fprintf(w, "%s %s\n", p.Green(ui.GlyphOK), p.Green(fmt.Sprintf("up to date: %d ops, no changes", plan.Total())))
	} else {
		fmt.Fprintf(w, "%s %s\n", p.Green(ui.GlyphOK), p.Green(fmt.Sprintf("applied: %d ops", plan.Total())))
	}
	report := render.BuildReport(c, plan, agents)
	if len(report.Rows) > 0 {
		fmt.Fprintln(w)
		report.PrintTextStyled(w, p)
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
// status, diff, reconcile, update re-apply) goes through it so they project and
// overlay identically.
//
// At project scope the project's own source tree (<root>/.agentsync/) is loaded
// as a full canonical and overlaid onto the user canonical via project.Merge —
// a missing tree loads as empty, so the overlay is a no-op. lenient selects the
// read-only/diagnostic projection: a strict same-name plugin.json/entry conflict
// is resolved entry-wins with a warning rather than a hard error, so status/diff
// still show state. Mutating callers pass false so a conflict aborts before any
// write.
func loadProjectedForScope(fs afero.Fs, home, scopeFlag, projectFlag string, lenient bool) (source.Canonical, adapter.Scope, string, error) {
	sc, projectRoot, err := resolveProjectScope(scopeFlag, projectFlag)
	if err != nil {
		return source.Canonical{}, sc, projectRoot, err
	}
	pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
	load := marketplace.LoadProjectedExcluding
	if lenient {
		load = marketplace.LoadProjectedLenient
	}
	// A project tree can suppress a (user- or project-scope) plugin's projected
	// components in this repo by carrying plugins/<id>.toml with `disabled = true`
	// — the dir-model successor to the M5 marker's `[plugins] disabled`. We read
	// those ids first and exclude them from BOTH projections so the plugin's MCP/
	// skills/hooks vanish at project scope (not just its record).
	var disabled []string
	projHome := ""
	if sc == adapter.ScopeProject && projectRoot != "" {
		projHome = project.Home(projectRoot)
		disabled, err = projectDisabledPlugins(fs, projHome)
		if err != nil {
			return source.Canonical{}, sc, projectRoot, fmt.Errorf("read project plugin disables: %w", err)
		}
	}
	c, err := load(fs, home, pluginCacheRoot, disabled)
	if err != nil {
		return source.Canonical{}, sc, projectRoot, err
	}
	if projHome != "" {
		pc, perr := load(fs, projHome, pluginCacheRoot, disabled)
		if perr != nil {
			return source.Canonical{}, sc, projectRoot, fmt.Errorf("load project source %s: %w", projHome, perr)
		}
		c = project.Merge(c, pc)
	}
	return c, sc, projectRoot, nil
}

// projectDisabledPlugins returns the plugin ids a project tree marks disabled
// (plugins/<id>.toml with `disabled = true`). A missing tree loads as empty.
func projectDisabledPlugins(fs afero.Fs, projHome string) ([]string, error) {
	pc, err := source.Load(fs, projHome)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pc.Plugins {
		if p.Plugin.Disabled {
			out = append(out, p.ID)
		}
	}
	return out, nil
}

// resolveProjectScope determines the effective scope and project root.
// Priority: --project flag > --scope flag > cwd walk-up auto-detect of a
// <root>/.agentsync/ tree.
func resolveProjectScope(scopeFlag, projectFlag string) (adapter.Scope, string, error) {
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

	// --scope project without --project: walk up from cwd to find a project tree.
	if scopeFlag == "project" {
		cwd, err := os.Getwd()
		if err != nil {
			return adapter.ScopeUser, "", fmt.Errorf("getwd: %w", err)
		}
		root, found, derr := discoverProjectTree(cwd)
		if derr != nil {
			return adapter.ScopeUser, "", fmt.Errorf("discover project: %w", derr)
		}
		if found {
			return adapter.ScopeProject, root, nil
		}
		// No project tree found; fall through to user scope.
		return adapter.ScopeUser, "", nil
	}

	// Default / --scope user: auto-detect from cwd.
	if scopeFlag == "" || scopeFlag == "user" {
		// Auto-detect: if cwd is inside a project tree, default to project scope.
		if scopeFlag == "" {
			cwd, err := os.Getwd()
			if err == nil {
				root, found, derr := discoverProjectTree(cwd)
				if derr == nil && found {
					return adapter.ScopeProject, root, nil
				}
			}
		}
		return adapter.ScopeUser, "", nil
	}

	return adapter.ScopeUser, "", fmt.Errorf("unknown --scope value %q; want user or project", scopeFlag)
}

// discoverProjectTree walks up from cwd for a <root>/.agentsync/ tree, but skips
// the user's OWN canonical home: ~/.agentsync/ is itself a .agentsync/ directory,
// so running from inside it (or from $HOME) must NOT be mistaken for a project —
// that would silently flip every command to project scope and stop writing the
// user-scope destinations. When the nearest match IS the user home, the search
// continues above it for a genuine project ancestor.
func discoverProjectTree(cwd string) (string, bool, error) {
	agentsyncHome := paths.AgentsyncHome(paths.OSEnv{})
	dir := cwd
	for {
		root, found, err := project.Discover(dir)
		if err != nil || !found {
			return "", false, err
		}
		if !sameDir(project.Home(root), agentsyncHome) {
			return root, true, nil
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", false, nil
		}
		dir = parent
	}
}

// sameDir reports whether a and b name the same directory, comparing cleaned
// absolute paths and (best-effort) their symlink-resolved forms so a symlinked
// temp root (macOS /tmp → /private/tmp) doesn't cause a false mismatch.
func sameDir(a, b string) bool {
	ca, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	cb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	ca, cb = filepath.Clean(ca), filepath.Clean(cb)
	if ca == cb {
		return true
	}
	if ra, rerr := filepath.EvalSymlinks(ca); rerr == nil {
		ca = ra
	}
	if rb, rerr := filepath.EvalSymlinks(cb); rerr == nil {
		cb = rb
	}
	return ca == cb
}
