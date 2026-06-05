package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	"golang.org/x/term"
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
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: user; prompts when run inside a project tree)")
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
	c, sc, projectRoot, err := loadProjectedForScope(cmd, afero.NewOsFs(), home, scopeFlag, projectFlag, false)
	if err != nil {
		return err
	}

	// Announce the effective scope so a project-scoped apply is never silent
	// about writing to a repo tree instead of the user's machine-wide config.
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
		// register an agent. Under project scope the likely cause is the
		// project tree's [agents] table resolving to nothing enabled, so
		// point there instead of at `agent add`.
		if sc == adapter.ScopeProject {
			fmt.Fprintf(p.Err,
				"%s no agents are enabled at project scope (%s); nothing to apply.\n"+
					"  Check the [agents] table in %s.\n", p.Yellow("agentsync:"), projectRoot, project.Home(projectRoot))
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
		report := render.BuildReport(reportCanonical(c, sc), plan, agents)
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
	report := render.BuildReport(reportCanonical(c, sc), plan, agents)
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
func loadProjectedForScope(cmd *cobra.Command, fs afero.Fs, home, scopeFlag, projectFlag string, lenient bool) (source.Canonical, adapter.Scope, string, error) {
	sc, projectRoot, err := resolveScope(cmd, scopeFlag, projectFlag, noInputFlag(cmd))
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

// resolveScope determines the effective scope and project root for a command.
// Project scope is always an EXPLICIT opt-in — agentsync never silently acts on
// a project tree just because cwd happens to sit inside one:
//
//   - --project <path>  → project scope rooted at <path>.
//   - --scope project   → project scope; walks up from cwd for a .agentsync/ tree
//     and ERRORS if none is found (never downgrades to user).
//   - --scope user      → user scope.
//   - (no scope flag)   → user scope, UNLESS cwd is inside a project tree, in
//     which case the choice is ambiguous: prompt interactively (no default), or
//     ERROR when non-interactive (--no-input, or stdin is not a TTY).
//
// For every command except init's own scaffolding, project scope requires the
// <root>/.agentsync/ tree to already exist; resolveScope returns an actionable
// error pointing at `agentsync init --scope project` otherwise.
func resolveScope(cmd *cobra.Command, scopeFlag, projectFlag string, noInput bool) (adapter.Scope, string, error) {
	switch {
	case projectFlag != "":
		// Explicit --project implies project scope, so --scope user alongside it
		// is contradictory — refuse rather than silently pick one.
		if scopeFlag == "user" {
			return adapter.ScopeUser, "", fmt.Errorf("--scope user conflicts with --project (which implies project scope); pass only one")
		}
		abs, err := filepath.Abs(projectFlag)
		if err != nil {
			return adapter.ScopeUser, "", fmt.Errorf("resolve --project path: %w", err)
		}
		if err := requireProjectTree(abs); err != nil {
			return adapter.ScopeUser, "", err
		}
		return adapter.ScopeProject, abs, nil

	case scopeFlag == "user":
		return adapter.ScopeUser, "", nil

	case scopeFlag == "project":
		cwd, err := os.Getwd()
		if err != nil {
			return adapter.ScopeUser, "", fmt.Errorf("getwd: %w", err)
		}
		root, found, derr := discoverProjectTree(cwd)
		if derr != nil {
			return adapter.ScopeUser, "", fmt.Errorf("discover project: %w", derr)
		}
		if !found {
			return adapter.ScopeUser, "", fmt.Errorf(
				"--scope project: no .agentsync/ project tree found at or above %s; "+
					"run `agentsync init --scope project` to create one", cwd,
			)
		}
		return adapter.ScopeProject, root, nil

	case scopeFlag == "":
		cwd, err := os.Getwd()
		if err != nil {
			// Can't inspect cwd — no project tree to detect; plain user scope.
			return adapter.ScopeUser, "", nil //nolint:nilerr // getwd failure degrades to the documented default
		}
		root, found, derr := discoverProjectTree(cwd)
		if derr != nil {
			return adapter.ScopeUser, "", fmt.Errorf("discover project: %w", derr)
		}
		if !found {
			return adapter.ScopeUser, "", nil
		}
		// Ambiguous: cwd is inside a project tree but no scope was requested.
		userHome := paths.AgentsyncHome(paths.OSEnv{})
		if noInput || !stdinIsTerminal(cmd) {
			return adapter.ScopeUser, "", fmt.Errorf(
				"a .agentsync/ project tree was detected at %s but no scope was given; "+
					"re-run with --scope project (apply it here) or --scope user (apply your user config) "+
					"— cannot prompt (non-interactive)", root,
			)
		}
		return promptScopeChoice(cmd, root, userHome)

	default:
		return adapter.ScopeUser, "", fmt.Errorf("unknown --scope value %q; want user or project", scopeFlag)
	}
}

// requireProjectTree errors unless <root>/.agentsync/ exists, so a non-init
// command never proceeds against a project root that was never scaffolded.
func requireProjectTree(root string) error {
	home := project.Home(root)
	fi, err := os.Stat(home)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("no .agentsync/ project tree at %s; "+
			"run `agentsync init --scope project --project %s` to create one", home, root)
	}
	return nil
}

// noInputFlag reads the inherited global --no-input flag. When set, ambiguous
// scope resolution fails closed instead of prompting (for headless scripts).
func noInputFlag(cmd *cobra.Command) bool {
	if v, err := cmd.Flags().GetBool("no-input"); err == nil {
		return v
	}
	if f := cmd.InheritedFlags().Lookup("no-input"); f != nil {
		return f.Value.String() == "true"
	}
	return false
}

// stdinIsTerminal reports whether the command's stdin is an interactive
// terminal (so a prompt would actually reach a human). A pipe, file, or the
// test harness's string reader is not — which routes scripts to the
// fail-closed path instead of a hung Read.
func stdinIsTerminal(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// promptScopeChoice asks the user to pick project vs user scope when cwd is
// inside a project tree and neither --scope nor --project was given. There is no
// default — an empty or invalid line re-prompts — so the choice is always a
// deliberate keystroke.
func promptScopeChoice(cmd *cobra.Command, projectRoot, userHome string) (adapter.Scope, string, error) {
	w := cmd.OutOrStdout()
	r := bufio.NewReader(cmd.InOrStdin())
	fmt.Fprintf(w, "ℹ this repo has a .agentsync/ project tree.\n")
	fmt.Fprintf(w, "  [1] project scope (%s)\n", projectRoot)
	fmt.Fprintf(w, "  [2] user scope (%s)\n", userHome)
	for attempts := 0; attempts < 5; attempts++ {
		fmt.Fprintf(w, "run `agentsync %s` at which scope? [1/2]: ", cmd.Name())
		line, err := r.ReadString('\n')
		switch strings.TrimSpace(line) {
		case "1":
			return adapter.ScopeProject, projectRoot, nil
		case "2":
			return adapter.ScopeUser, "", nil
		}
		if err != nil {
			// EOF / closed stdin with no valid choice — don't loop forever.
			return adapter.ScopeUser, "", fmt.Errorf("no scope selected (input closed); pass --scope user|project")
		}
		fmt.Fprintf(w, "  please enter 1 (project) or 2 (user).\n")
	}
	return adapter.ScopeUser, "", fmt.Errorf("no valid scope selected after 5 attempts; pass --scope user|project")
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

// reportCanonical returns the canonical to pass to render.BuildReport. At
// project scope the report should reflect what was actually rendered (the
// project-only overlay), not the merged canonical which includes user-scope
// items the adapters never wrote to the project directory.
func reportCanonical(c source.Canonical, sc adapter.Scope) source.Canonical {
	if sc == adapter.ScopeProject && c.Project != nil {
		return *c.Project
	}
	return c
}
