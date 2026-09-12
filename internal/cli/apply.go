package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
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
		noGitBackup bool
		agentsCSV   string
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "render canonical config and write per agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			o := applyOpts{dryRun: dryRun, noGitBackup: noGitBackup, agentsCSV: agentsCSV}
			// Dry-run is read-only — it touches neither destinations nor
			// state. Acquiring the global lock would needlessly block
			// concurrent `status` / `diff` / other dry-runs behind a long
			// real apply.
			if dryRun {
				return runApplyPipeline(cmd, home, o)
			}
			return withGlobalLock(home, func() error {
				return runApplyPipeline(cmd, home, o)
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compute plan without writing destinations")
	markScopeAware(cmd)
	cmd.Flags().BoolVar(&noGitBackup, "no-git-backup", false, "skip destination git versioning/checkpoint for this run (CI/scripting); does not modify agentsync.toml")
	addAgentsFlag(cmd, &agentsCSV, "apply")
	return cmd
}

// noAgentsEnabledHint is the shared "nothing is enabled" notice for read-only
// commands (status/diff). At project scope it must point at the PROJECT's
// [agents] declaration — the user-scope `agent add` hint would send the user
// to edit the wrong file.
func noAgentsEnabledHint(sc adapter.Scope, projectRoot string) string {
	if sc == adapter.ScopeProject {
		return fmt.Sprintf("no agents are enabled at project scope (%s); check the [agents] table in %s or run `agentsync agent enable <name> --scope project`",
			projectRoot, filepath.Join(project.Home(projectRoot), "agentsync.toml"))
	}
	return "no agents enabled; run `agentsync agent add claude` (or opencode)"
}

// applyOpts carries the per-run knobs of the apply pipeline. The ZERO VALUE is
// a plain real apply of every enabled agent, not opted out of destination git
// backup (the [destination_directory_git_backup] mode still governs it) —
// which is exactly what a caller that is not the `apply` command wants. The
// three fields are exactly `apply`'s three flags and nothing else: a behaviour
// gate added here would recreate, one field at a time, the divergence #231
// closed. Anything a caller wants to say differently is said at the call site,
// before or after the call.
type applyOpts struct {
	// dryRun is `apply --dry-run`: compute and print the plan, write nothing,
	// skip the git backup.
	dryRun bool
	// noGitBackup is `apply --no-git-backup`: skip the destination git
	// baseline/checkpoint for this run only.
	noGitBackup bool
	// agentsCSV is the raw `--agents` value. selectAgents consults it only when
	// the CALLING command reports cmd.Flags().Changed("agents") — the one
	// non-persistent flag the pipeline reads (every other cmd read inside is a
	// root persistent flag: --color, --scope, --project, --no-input). pflag
	// answers false for a flag the command never registered, so a caller whose
	// command does not define `--agents` gets every enabled agent whatever this
	// holds (pinned by TestPluginUpgrade_RendersEveryEnabledAgent). The residual
	// is a caller whose command defines a same-named `--agents` with a
	// DIFFERENT meaning (`mcp add --agents`): it must pass an explicit
	// agentsCSV rather than rely on the flag being absent.
	agentsCSV string
}

// runApplyPipeline is the apply pipeline — load-projected source → resolve
// secrets → plan → git baseline → write → record state → checkpoint → report —
// and the body of the apply command (callers must hold the global lock; only
// `apply --dry-run`, which writes nothing, runs without it). It is called by
// BOTH `apply` and the re-apply tail of `plugin upgrade`
// (reapplyAfterPluginChange) so the two cannot diverge: the second copy had
// already lost the pre-apply baseline and checkpoint (#118/#143), the
// removal-aware headline, the backup pruning and the translation report
// (#231). home is the agentsync home; the printer, scope, secrets backend and
// state path are all derived inside, so a caller cannot hand in a stale one.
func runApplyPipeline(cmd *cobra.Command, home string, o applyOpts) error {
	p, err := newPrinter(cmd)
	if err != nil {
		return err
	}
	c, sc, projectRoot, err := loadProjectedForScope(cmd, afero.NewOsFs(), home, false)
	if err != nil {
		return err
	}

	// Announce the effective scope so a project-scoped apply is never silent
	// about writing to a repo tree instead of the user's machine-wide config.
	if sc == adapter.ScopeProject {
		p.Infof("scope: project (%s)", projectRoot)
	} else {
		p.Infof("scope: user")
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

	agents, enabled := enabledAgentNames(c.Config)
	// --agents narrows the apply to a validated allowlist, with the SAME parsing
	// status/diff use (#200 F10). Applied after the enabled set is built, so an
	// unknown or disabled name is rejected rather than silently rendering nothing.
	if len(agents) > 0 {
		sel, aerr := selectAgents(cmd, agents, enabled, o.agentsCSV)
		if aerr != nil {
			return aerr
		}
		agents = sel
	}
	if len(agents) == 0 {
		// Without this hint, `apply` prints "applied: 0 ops" and a
		// new user assumes their config "worked". Tell them how to
		// register an agent. Under project scope the likely cause is the
		// project tree's [agents] table resolving to nothing enabled, so
		// point there instead of at `agent add`.
		if sc == adapter.ScopeProject {
			p.Warnf("no agents are enabled at project scope (%s); nothing to apply.", projectRoot)
			p.Detailf("Check the [agents] table in %s.", project.Home(projectRoot))
		} else {
			p.Warnf("no agents are enabled in agentsync.toml; nothing to apply.")
			p.Detailf("Run `agentsync agent add claude` (or opencode) to register an agent.")
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

	if o.dryRun {
		plan, err := render.Plan(resolved, reg, agents, sc, projectRoot, s, userHome)
		if err != nil {
			return err
		}
		// Run the pipeline through non-writing preview writers so we know, per
		// destination, whether the real apply would actually change it (and back
		// up any foreign content first) or find it already in sync. Without this
		// the dry-run labels every op "write" even when nothing would change.
		previews, _, wouldChange, perr := render.PreviewApply(plan, reg, s, home, userHome, sc, projectRoot)
		if perr != nil {
			return perr
		}
		w := p.Out
		toWrite, synced, removalOps := planSyncCounts(plan, wouldChange)
		// The three counts partition plan.Total(), so the headline always sums.
		if removalOps > 0 {
			fmt.Fprintf(w, "%s %d ops total across %d agent(s) — %d to write, %d already synced, %d removal op(s)\n",
				p.Bold("Plan:"), plan.Total(), len(plan.PerAgent), toWrite, synced, removalOps)
		} else {
			fmt.Fprintf(w, "%s %d ops total across %d agent(s) — %d to write, %d already synced\n",
				p.Bold("Plan:"), plan.Total(), len(plan.PerAgent), toWrite, synced)
		}
		// Preview convergence-time removals with the same counts the real apply
		// reports in its headline, so a delete-only run no longer previews as
		// "Plan: 0 ops … 0 to write" and then surprises with "removed: …" on the
		// real run. Dry-run never prunes state, so the state entries
		// OrphanDeletes reads are still intact here.
		if removedKeys, removedFiles, _ := removalCounts(plan, s, userHome, sc, projectRoot); removedKeys+removedFiles > 0 {
			fmt.Fprintf(w, "%s %s (the real apply will remove these)\n",
				p.Bold("Removals:"), removedLabel(removedKeys, removedFiles))
		}
		for _, name := range reg.Names() {
			res, ok := plan.PerAgent[name]
			if !ok {
				continue
			}
			fmt.Fprintf(w, "  %s %d ops, %d skips\n", p.Bold(ui.Pad(name, 10)), len(res.Ops), len(res.Skips))
			// List every destination path so the user can see exactly what
			// apply will touch — and, for each, whether it would be written or
			// is already in sync, so a clean re-apply reads as a no-op instead
			// of a wall of "write"s.
			for _, op := range res.Ops {
				printPlannedOp(w, p, op, wouldChange)
			}
		}
		// Foreign-collision preview: which destinations contain content
		// that agentsync does not own and will therefore be backed up
		// before overwrite. The dry-run previously hid this; users only
		// found out which files were about to be backed up after the
		// real apply ran.
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

	// Count convergence-time removals NOW, before PruneStaleState drops the
	// state entries OrphanDeletes reads — so the summary can report
	// "removed: N key(s)/file(s)" instead of mislabeling a delete-only run as
	// "up to date" / "applied: 0 ops".
	removedKeys, removedFiles, appliedOps := removalCounts(plan, s, userHome, sc, projectRoot)
	removed := removedKeys + removedFiles

	// Pre-apply baseline (issue #143): BEFORE render.Apply overwrites anything,
	// commit the current on-disk state of each version root this apply will write
	// into, so even the FIRST apply is revertible — the apply checkpoint's parent is
	// the genuine pre-apply state. The baseline shares ONE session with the post-apply
	// checkpoint below so a fresh dir is inited/prompted exactly once. Best-effort with
	// a loud warning — a baseline failure never aborts the apply (honors
	// --no-git-backup / mode=off / project scope / a declined prompt, all as nil).
	gb := newGitBackupSession(cmd, p, reg, agents, sc, projectRoot, home, c.Config.DestinationGitBackup, o.noGitBackup)
	gb.baseline(baselinePaths(plan, s, userHome, sc, projectRoot))

	collisions, written, unchanged, applyErr := render.Apply(plan, reg, s, home, userHome, sc, projectRoot)
	if len(collisions) > 0 {
		p.Warnf("backed up %d pre-existing target(s) before overwriting:", len(collisions))
		for _, r := range collisions {
			p.Detailf("%s", r.String())
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
	// Post-apply state.Save. If it FAILS here, every destination is already
	// written correctly but its ownership was not recorded — the dests are
	// "written-but-unrecorded". This is NOT silent data loss: the next apply
	// treats each unrecorded dest as FOREIGN (state owns nothing there) and so
	// backs it up before overwriting (render.Writer.maybeBackup), then records
	// it — a self-heal. The apply-error rescue above ends in its own Save for the
	// mid-pipeline case; this is the all-writes-succeeded case. See
	// docs/architecture.md (apply pipeline) for the durability argument.
	if err := state.Save(statePath, s); err != nil {
		return err
	}
	// Bound backup growth (each is a verbatim, possibly-secret-bearing copy
	// of a pre-existing native file). Best-effort; never fails the apply.
	_ = render.PruneBackups(home, render.DefaultBackupKeep)

	// Destination git backup (issue #118): checkpoint the user-scope agent dirs we
	// just wrote into their own local-only git repos, so a bad apply is an
	// `agentsync revert` away. Never pushed. Best-effort — a git failure here must
	// not fail an apply whose files are already written and state already saved. This
	// is the SAME session that took the pre-apply baseline above, so a fresh dir it
	// inited is committed into here without a second prompt.
	if err := gb.checkpoint(written); err != nil {
		p.Warnf("destination git backup: %v", err)
	}

	w := p.Out
	// Headline honesty, four cases:
	//   - removals only (a skill/MCP/key removed from source, no real writes) →
	//     "removed: N key(s), M file(s)"; never "up to date" / "applied: 0 ops".
	//   - writes AND removals → "applied: X ops, removed: N key(s), M file(s)".
	//   - a genuine clean re-apply (every dest already held our exact bytes,
	//     nothing removed) → "up to date" (unchanged detection preserved).
	//   - normal writes → "applied: N ops".
	//   - a zero-op plan (no agent renders anything yet) → a plain ✅, not 🎉:
	//     celebrating an apply that did nothing reads as a tool that does not
	//     know what it did.
	switch {
	case removed > 0 && appliedOps == 0:
		p.Fsuccessf(w, ui.EmojiRemoved, "removed: %s", removedLabel(removedKeys, removedFiles))
	case removed > 0:
		p.Fsuccessf(w, ui.EmojiApplied, "applied: %d ops, removed: %s", appliedOps, removedLabel(removedKeys, removedFiles))
	case len(written) > 0 && len(unchanged) == len(written):
		// Nothing changed, so this is not a celebration — a plain ✅ says
		// "checked, all good" without implying work was done.
		p.Fsuccessf(w, ui.EmojiSuccess, "up to date: %d ops, no changes", plan.Total())
	case plan.Total() == 0:
		// Nothing to do at all (no agents render anything yet). Celebrating a
		// zero-op apply reads as a tool that doesn't know what it did.
		p.Fsuccessf(w, ui.EmojiSuccess, "applied: 0 ops")
	default:
		p.Fsuccessf(w, ui.EmojiApplied, "applied: %d ops", plan.Total())
	}
	report := render.BuildReport(reportCanonical(c, sc), plan, agents)
	if len(report.Rows) > 0 {
		fmt.Fprintln(w)
		report.PrintTextStyled(w, p)
	}
	return nil
}

// printPlannedOp renders one planned destination op in the `apply --dry-run`
// listing. A write op whose destination already holds our exact bytes (it is
// NOT in wouldChange, computed by render.PreviewApply) is labeled "synced" with
// a green check, so a clean re-apply reads as a no-op instead of a wall of
// pending "write"s; anything that would actually change keeps the cyan arrow.
func printPlannedOp(w io.Writer, p *ui.Printer, op adapter.FileOp, wouldChange map[string]bool) {
	// op.Path embeds a config-derived component name/id; sanitize on display so
	// an ESC in a shared config's name can't inject escapes into the plan preview
	// (issue #93/#171).
	dispPath := ui.Sanitize(op.Path)
	// An orphan-cleanup op (adapter.OpCleanup, stamped at synthesis) is a key
	// REMOVAL: it is excluded from the "to write" headline count
	// (planSyncCounts) and summarized under "Removals:", so labeling it "write"
	// here would make the listing disagree with both. Checked BEFORE isSyncedOp
	// — planSyncCounts classifies it as a removal first too, and an
	// already-converged cleanup op printing "synced" would reopen the same
	// listing/headline split.
	if op.Kind == adapter.OpCleanup {
		fmt.Fprintf(w, "    %s %s %s\n", p.Yellow(ui.GlyphArrow), p.Yellow(ui.Pad("remove", 6)), dispPath)
		return
	}
	if isSyncedOp(op, wouldChange) {
		fmt.Fprintf(w, "    %s %s %s\n", p.Green(ui.GlyphOK), p.Green(ui.Pad("synced", 6)), dispPath)
		return
	}
	fmt.Fprintf(w, "    %s %s %s\n", p.Cyan(ui.GlyphArrow), p.Cyan(ui.Pad(op.Action.String(), 6)), dispPath)
}

// isSyncedOp reports whether a planned op is a write the destination already
// satisfies — i.e. a real apply would skip it. Delete ops, and any write whose
// destination would be created or modified, are never "synced".
func isSyncedOp(op adapter.FileOp, wouldChange map[string]bool) bool {
	if op.Action != adapter.ActionWrite {
		return false
	}
	return !wouldChange[op.Path]
}

// planSyncCounts splits the plan's ops into how many a real apply would write
// (or delete) versus how many are already in sync, so the dry-run summary line
// can quantify what the per-op labels show.
func planSyncCounts(plan render.RenderPlan, wouldChange map[string]bool) (toWrite, synced, removals int) {
	for _, res := range plan.PerAgent {
		for _, op := range res.Ops {
			// An orphan-cleanup op (adapter.OpCleanup — the same kind
			// removalCounts reads) is previewed under "Removals:", and the real
			// apply's headline subtracts it from "applied: X ops" — counting it
			// in "to write" too made the dry-run and real headlines disagree by
			// one per cleanup op. It is returned as its own count so the Plan
			// line still sums to the total.
			if op.Kind == adapter.OpCleanup {
				removals++
				continue
			}
			if isSyncedOp(op, wouldChange) {
				synced++
			} else {
				toWrite++
			}
		}
	}
	return toWrite, synced, removals
}

// removalCounts inspects the plan (against the pre-apply state) for the two kinds
// of convergence-time removals `apply` performs, so the summary headline can
// report them instead of mislabeling a delete-only run "up to date" / "applied: 0
// ops":
//
//   - removedFiles: whole-file component deletes (a skill, a bundled file within
//     one, a subagent, or a command removed from — or renamed in — source),
//     surfaced by render.OrphanDeletes and deduped across agents that share a
//     destination dir; and
//   - removedKeys: orphan-cleanup key removals — an emptied merge section renders
//     as a "{}" key-merge op carrying the owned pointers to drop
//     (render.orphanCleanupOps), so each such op removes len(OwnedKeys) keys
//     rather than writing anything.
//
// The two counts are returned separately so the headline can label them
// distinctly (a deleted key is not an "op" in the same sense a deleted file is).
// appliedOps is plan.Total() with the cleanup (OpCleanup) ops subtracted, so a
// mixed run reports the removal under "removed:" and does not also count it as an
// applied write. MUST be called BEFORE PruneStaleState, which drops the state
// entries OrphanDeletes reads. (Dry-run never prunes, so it can call this
// at any point.)
func removalCounts(plan render.RenderPlan, s *state.Targets, userHome string, sc adapter.Scope, projectRoot string) (removedKeys, removedFiles, appliedOps int) {
	appliedOps = plan.Total()
	fileDeletes := map[string]bool{}
	for name, res := range plan.PerAgent {
		for _, del := range render.OrphanDeletes(s, userHome, name, sc, projectRoot, res.Ops) {
			// Count only what apply will actually remove. An orphan whose
			// destination cannot be read is SKIPPED with a warning, and its state
			// entry is kept so the next run retries — so counting it here would
			// print "removed: N" on every run alongside the warning saying it was
			// not removed.
			if render.OrphanDeleteWillProceed(del) {
				fileDeletes[del.Path] = true
			}
		}
		for _, op := range res.Ops {
			// An orphan-cleanup op (adapter.OpCleanup, stamped by
			// adapter.NewCleanupOp) carries the owned pointers to delete and
			// writes nothing else.
			if op.Kind == adapter.OpCleanup {
				removedKeys += len(op.OwnedKeys)
				appliedOps-- // a removal, not an applied write
			}
		}
	}
	removedFiles = len(fileDeletes)
	if appliedOps < 0 {
		appliedOps = 0
	}
	return removedKeys, removedFiles, appliedOps
}

// removedLabel renders the removal counts with the two kinds labeled
// distinctly — "2 key(s), 1 file(s)" — omitting a zero count. Callers only use
// it when keys+files > 0.
func removedLabel(keys, files int) string {
	var parts []string
	if keys > 0 {
		parts = append(parts, fmt.Sprintf("%d key(s)", keys))
	}
	if files > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s)", files))
	}
	return strings.Join(parts, ", ")
}

// baselinePaths returns the set of destination paths the plan will WRITE or
// DELETE — every explicit op regardless of action, plus the writer-derived
// skill orphan deletes — so the pre-apply baseline pass (issue #143) knows
// which paths' pre-apply content to stage BEFORE render.Apply runs. Planned
// deletes MUST be included: the writer removes an in-sync orphan without a
// backup (it is agentsync's own output), so a deleted file's bytes land in no
// checkpoint unless the baseline captured them — omitting them made the first
// git-backed apply's deletions unrecoverable. It is a superset of the eventual
// `written` set (a planned op may turn out already-in-sync), which is exactly
// right for deciding which roots to baseline. MUST be called before
// render.PruneStaleState, which drops the state entries OrphanDeletes
// reads.
func baselinePaths(plan render.RenderPlan, s *state.Targets, userHome string, sc adapter.Scope, projectRoot string) map[string]bool {
	out := map[string]bool{}
	for name, res := range plan.PerAgent {
		for _, op := range res.Ops {
			out[op.Path] = true
		}
		for _, del := range render.OrphanDeletes(s, userHome, name, sc, projectRoot, res.Ops) {
			out[del.Path] = true
		}
	}
	return out
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
