package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
	"github.com/spxrogers/agentsync/internal/untrusted"
	"github.com/tailscale/hujson"
)

// statusItem is one tracked file or merged key and its drift classification.
type statusItem struct {
	Path string `json:"path"`
	// Pointer is the RFC-6901 JSON pointer for a key-merge item; empty for a
	// whole-file item.
	Pointer string `json:"pointer,omitempty"`
	Class   string `json:"class"`
}

// statusAgent groups one agent's tracked items.
type statusAgent struct {
	Agent string       `json:"agent"`
	Items []statusItem `json:"items"`
}

// statusModel is the full drift report, rendered either as the formatted
// dashboard or, under --json, verbatim. Summary tallies items by drift class.
type statusModel struct {
	Agents  []statusAgent  `json:"agents"`
	Summary map[string]int `json:"summary"`
}

// exitCodeDrift is the process exit code `status`/`diff` return under
// --exit-code when drift (status) or hunks (diff) exist. It is deliberately
// distinct from the generic error exit (1) so a CI gate can tell "drift
// detected" apart from "the command itself failed".
const exitCodeDrift = 2

// ExitCoder is implemented by the quiet sentinel error `status`/`diff` return
// under --exit-code. main() maps it to a process exit code and prints nothing
// (the sentinel's Error() is empty), so a CI gate gets a stable non-zero exit
// without a spurious "agentsync: ..." line. The root command already sets
// SilenceErrors, so cobra prints nothing for it either.
type ExitCoder interface{ ExitCode() int }

// exitCodeError is a quiet sentinel carrying a process exit code. Its message
// is empty on purpose: the drift signal is the exit code, not stderr text.
type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return "" }
func (e *exitCodeError) ExitCode() int { return e.code }

// newDriftExitError returns the quiet --exit-code sentinel (exitCodeDrift).
func newDriftExitError() error { return &exitCodeError{code: exitCodeDrift} }

// statusHasDrift reports whether the model contains any item that is not fully
// in sync — i.e. any class other than clean/converged. It is the --exit-code
// predicate: a CI gate wants a non-zero exit whenever the tree is not exactly
// what a fresh apply would produce (drift/conflict/orphan) OR carries unapplied
// changes (new/pending).
func statusHasDrift(m statusModel) bool {
	for cls, n := range m.Summary {
		if n == 0 {
			continue
		}
		if cls == drift.Clean.String() || cls == drift.Converged.String() {
			continue
		}
		return true
	}
	return false
}

func newStatusCmd() *cobra.Command {
	var (
		jsonOut   bool
		exitCode  bool
		legend    bool
		agentsCSV string
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "report drift across registered agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd)
			if err != nil {
				return err
			}
			// --legend is a standalone reference dump: no project/state/secrets
			// to load, no scope to resolve. Handle it before any of that work.
			// It must reject combination with any flag whose contract it would
			// otherwise silently break — --json (a script expecting a parseable
			// payload would get prose on stdout), --exit-code (would always see
			// exit 0, masking real drift), and --agents (would look accepted but
			// have no effect) — rather than accept-and-ignore them. --scope/
			// --project are deliberately left alone here: unlike the three
			// above, they have no status-specific contract to silently break —
			// the glossary is the same nine classes regardless of scope — so
			// accepting and ignoring them is correct, not a footgun.
			if legend {
				switch {
				case jsonOut:
					return fmt.Errorf("--legend cannot be combined with --json")
				case exitCode:
					return fmt.Errorf("--legend cannot be combined with --exit-code")
				case cmd.Flags().Changed("agents"):
					// Changed(), not agentsCSV != "" — selectAgents treats an
					// explicitly-empty `--agents ""` as an error too (a script
					// bug, not "no agents"), and this rejection must match: a
					// plain agentsCSV != "" check would let
					// `--legend --agents ""` through silently, still ignoring
					// the flag it names.
					return fmt.Errorf("--legend cannot be combined with --agents")
				}
				renderClassLegend(p)
				return nil
			}
			home := paths.AgentsyncHome(paths.OSEnv{})
			// Load WITH the plugin cache so drift classification sees the
			// same plugin-projected components `apply` writes; source.Load
			// alone would report plugin-managed files/keys as untracked.
			userHome := paths.HomeDir(paths.OSEnv{})
			c, sc, projectRoot, err := loadProjectedForScope(cmd, afero.NewOsFs(), home, true)
			if err != nil {
				return err
			}

			statePath := filepath.Join(home, ".state", "targets.json")
			s, err := state.Load(statePath)
			if err != nil {
				return err
			}
			reg := registryFactory()
			var enabledAgents []string
			enabled := map[string]bool{}
			for name, ag := range c.Config.Agents {
				if ag.Enabled {
					enabledAgents = append(enabledAgents, name)
					enabled[name] = true
				}
			}
			// --agents narrows the report (and the plan) to the requested
			// agent(s); orphan-state warnings still consider the FULL enabled
			// set so a deselected agent isn't mistaken for an orphaned one.
			selected, aerr := selectAgents(cmd, enabledAgents, enabled, agentsCSV)
			if aerr != nil {
				return aerr
			}
			if len(selected) == 0 {
				if jsonOut {
					emitStatusWarnings(p, c, reg, s, enabledAgents, selected, sc)
					return emitJSON(p.Out, statusModel{Agents: []statusAgent{}, Summary: map[string]int{}})
				}
				fmt.Fprintln(p.Out, noAgentsEnabledHint(sc, projectRoot))
				emitStatusWarnings(p, c, reg, s, enabledAgents, selected, sc)
				return nil
			}
			// apply WRITES (and RecordOpsState HASHES) the secret-RESOLVED
			// content, so status must hash the resolved source too — otherwise a
			// synced ${secret:…}/${env:…} item compares templated-vs-resolved and
			// classifies as phantom "pending" forever. Resolve like apply; fall
			// back to the templated render only when the backend is unavailable
			// (locked age key / CI), preserving offline status at the cost of the
			// pre-existing false-pending in that degraded mode. Resolved values
			// are only hashed here, never printed.
			rendered := secrets.ForRender(c)
			secBackend := secrets.SelectBackend(c.Config.Secrets, home, userHome)
			if resolved, serr := secrets.SubstituteCanonical(c, secBackend, secrets.EnvBackend{}); serr == nil {
				rendered = resolved
			}
			plan, err := render.Plan(rendered, reg, selected, sc, projectRoot, s, userHome)
			if err != nil {
				return err
			}

			model := buildStatusModel(plan, reg.Names(), s, userHome, sc, projectRoot)
			if jsonOut {
				// JSON to stdout, advisory warnings to stderr — keeps the
				// machine-readable payload cleanly parseable. The --json payload
				// is never collapsed: it carries every tracked item so scripts
				// see the same per-file model regardless of the human view.
				emitStatusWarnings(p, c, reg, s, enabledAgents, selected, sc)
				if err := emitJSON(p.Out, model); err != nil {
					return err
				}
			} else {
				renderStatusText(p, model, statusVerbose(cmd))
				emitStatusWarnings(p, c, reg, s, enabledAgents, selected, sc)
			}
			// --exit-code turns status into a CI gate: a non-zero (documented,
			// stable) exit when any item is not clean/converged, exit 0 when the
			// tree is fully in sync. The report above is emitted first either way,
			// so the human/JSON output is unchanged — only the process exit differs.
			if exitCode && statusHasDrift(model) {
				return newDriftExitError()
			}
			return nil
		},
	}
	markScopeAware(cmd)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON instead of the formatted report")
	cmd.Flags().BoolVar(&exitCode, "exit-code", false, fmt.Sprintf("exit %d if any drift is detected (0 when clean); for CI gates", exitCodeDrift))
	cmd.Flags().BoolVar(&legend, "legend", false, "print a glossary of every drift classification status and exit")
	addAgentsFlag(cmd, &agentsCSV, "report")
	return cmd
}

// statusVerbose reports whether the inherited global -v/--verbose flag is set.
// In status, verbose expands the default collapsed skill-directory rows back
// into one line per bundled file (the pre-collapse view). It reads the flag off
// the merged set, falling back to the inherited set the same way newPrinter
// reads --color.
func statusVerbose(cmd *cobra.Command) bool {
	if b, err := cmd.Flags().GetBool("verbose"); err == nil {
		return b
	}
	if f := cmd.InheritedFlags().Lookup("verbose"); f != nil {
		return f.Value.String() == "true"
	}
	return false
}

// resolveAgentFilter validates the (already comma-split) --agents values against
// the known agent set and the currently-enabled set, returning the de-duplicated
// selection in the order given. An unknown name or an agent that exists but is
// not enabled is a hard error — silently dropping it would make
// `status --agents typo` look clean. Callers split + reject the empty case
// before calling (mirroring `mcp add --agents`), so a non-empty input that
// resolves to nothing here is itself an error.
func resolveAgentFilter(want []string, enabled map[string]bool) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, a := range want {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		if !isValidAgent(a) {
			return nil, fmt.Errorf("unknown agent %q; valid agents: %s", a, validAgentsList())
		}
		if !enabled[a] {
			return nil, fmt.Errorf("agent %q is not enabled; run `agentsync agent add %s` first", a, a)
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf(`--agents cannot be empty; pass "*" for all enabled agents or name one or more`)
	}
	return out, nil
}

// containsStar reports whether names includes the "*" wildcard ("all enabled").
func containsStar(names []string) bool {
	for _, n := range names {
		if strings.TrimSpace(n) == "*" {
			return true
		}
	}
	return false
}

// buildStatusModel classifies every tracked file/key/orphan across agents into
// the structured statusModel. It is the single source of truth both the
// formatted dashboard and --json render from.
func buildStatusModel(plan render.RenderPlan, names []string, s *state.Targets, userHome string, sc adapter.Scope, projectRoot string) statusModel {
	model := statusModel{Summary: map[string]int{}}
	for _, name := range names {
		res, ok := plan.PerAgent[name]
		if !ok {
			continue
		}
		ag := statusAgent{Agent: name}
		seen := map[string]bool{}
		// file-level: every non-key-merge op is a whole-file item (including the
		// "replace" strategy used by skills/subagents/commands/memory).
		for _, op := range res.Ops {
			if render.IsKeyMerge(op.MergeStrategy) {
				continue // covered key-by-key below
			}
			if seen[op.Path] {
				continue
			}
			seen[op.Path] = true
			entry := s.Files[stateFileKey(userHome, name, sc, projectRoot, op.Path)]
			hsrc := hashContent(op.Content)
			happlied := entry.SHA256
			hdest := hashFile(op.Path)
			cls := drift.Classify(hsrc, happlied, hdest).String()
			// A file whose CONTENT is clean but whose permission bits drifted from
			// what agentsync last applied is still drift — the next apply re-
			// converges the mode (render.Writer.Write chmods a content-identical
			// file whose mode differs). Without this, a skill script that lost its
			// +x bit reports "clean" yet the next apply would change it.
			if cls == drift.Clean.String() && modeDrifted(entry.Mode, op.Path) {
				cls = drift.Drift.String()
			}
			ag.Items = append(ag.Items, statusItem{Path: op.Path, Class: cls})
			model.Summary[cls]++
		}
		// key-level: walk owned pointers for each merge op.
		for _, op := range res.Ops {
			if !render.IsKeyMerge(op.MergeStrategy) {
				continue
			}
			var ours map[string]any
			_ = json.Unmarshal(op.Content, &ours)
			final := readDestFile(op.MergeStrategy, op.Path)
			for _, ptr := range render.CollectPointers(ours, "") {
				hsrc := hashAnyValue(getPointerValue(ours, ptr))
				happlied := s.Keys[stateKeyKey(userHome, name, sc, projectRoot, op.Path, ptr)].SHA256
				hdest := hashAnyValue(getPointerValue(final, ptr))
				cls := drift.Classify(hsrc, happlied, hdest).String()
				ag.Items = append(ag.Items, statusItem{Path: op.Path, Pointer: ptr, Class: cls})
				model.Summary[cls]++
			}
		}
		// orphans: whole-file dests this agent still owns in state but no longer
		// renders (the source component was removed). Without these, status
		// reports nothing for a file that lingers and the next apply/reconcile
		// would act on.
		for _, orphan := range render.OrphanFiles(s, userHome, name, sc, projectRoot, res.Ops) {
			happlied := s.Files[stateFileKey(userHome, name, sc, projectRoot, orphan)].SHA256
			cls := drift.Classify("", happlied, hashFile(orphan)).String()
			ag.Items = append(ag.Items, statusItem{Path: orphan, Class: cls})
			model.Summary[cls]++
		}
		model.Agents = append(model.Agents, ag)
	}
	return model
}

// classOrder is the stable display order for the summary footer and
// `status --legend` (coloring is styleClass's job, not this ordering's). It
// is also, deliberately, an EXHAUSTIVE list of all nine drift classes —
// renderClassLegend relies on that to enumerate every row, and
// TestClassTablesCoverAllDriftClasses is what actually guards it.
var classOrder = []string{
	"clean", "converged", "new", "pending",
	"drift", "conflict", "foreign-collision", "orphan", "orphan-drifted",
}

// classSeverity ranks drift classes most-severe-first. A collapsed skill row
// shows the most severe class among its members as its headline so a single
// drifted/conflicted SKILL.md inside an otherwise-clean skill still surfaces in
// red at the collapsed level — the count of files never hides a problem.
var classSeverity = []string{
	"orphan-drifted", "conflict", "drift", "foreign-collision",
	"orphan",
	"pending", "new",
	"converged", "clean",
}

// skillGroup is a set of tracked items that all live under one skill directory
// (…/skills/<name>/). The default `status` view renders it as a single line —
// the skill dir, its most-severe class, and a faint file-count summary —
// instead of one line per bundled SKILL.md/script/reference/asset.
type skillGroup struct {
	Root  string
	Items []statusItem
}

// displayClass maps a drift class to what the formatted `status` dashboard
// shows the user. "converged" folds into "clean": both mean apply has nothing
// left to do, and the distinction (converged = source AND destination changed
// independently but landed on the same value, vs. clean = neither changed) is
// bookkeeping that matters to the internal classifier and `status --json`, not
// to someone scanning the report. `status --legend` still explains converged
// on its own — see classMeaning and renderClassLegend. Compares against
// drift.Converged/drift.Clean's own String() rather than string literals, so
// a rename of either constant can't silently break the fold while every test
// stays green.
//
// explain_print.go's driftStyled deliberately does NOT apply this fold — it
// reuses the classifier's vocabulary verbatim by design (see its doc comment)
// — so the same item can read "clean" under `status` and "converged" under
// `explain`. That's an accepted, documented divergence between the two
// surfaces, not something this function should paper over.
func displayClass(cls string) string {
	if cls == drift.Converged.String() {
		return drift.Clean.String()
	}
	return cls
}

// styleClass maps a drift class to its glyph and color. Green = synced, cyan =
// a pending change apply will make, red = unexpected/destructive drift, yellow
// = an orphan needing a decision.
func styleClass(p *ui.Printer, cls string) (glyph string, color func(string) string) {
	switch cls {
	case drift.Clean.String(), drift.Converged.String():
		return ui.GlyphOK, p.Green
	case "new", "pending":
		return ui.GlyphArrow, p.Cyan
	case "drift", "conflict", "foreign-collision", "orphan-drifted":
		return ui.GlyphErr, p.Red
	case "orphan":
		return ui.GlyphWarn, p.Yellow
	default:
		return ui.GlyphInfo, p.Faint
	}
}

// renderStatusText prints the formatted drift dashboard: a bold header per
// agent, a glyph + color-coded class per item, and a one-line summary footer.
// By default each skill directory collapses to a single row so a tree of
// hundreds of bundled files stays readable; verbose restores the per-file view.
func renderStatusText(p *ui.Printer, model statusModel, verbose bool) {
	collapsed := 0
	for _, ag := range model.Agents {
		fmt.Fprintln(p.Out, p.Bold("["+ag.Agent+"]"))
		// An enabled agent that renders nothing (no components target it yet)
		// still gets a header row; without a note it reads as a truncated/broken
		// listing. Say so explicitly instead of leaving a bare "[agent]" line.
		if len(ag.Items) == 0 {
			fmt.Fprintln(p.Out, p.Faint("  (no tracked items)"))
			continue
		}
		collapsed += renderAgentItems(p, ag.Items, verbose)
	}

	// Summary footer lists only non-zero classes, so the words "drift" /
	// "conflict" / "pending" never appear when there is none of that state.
	// converged folds into clean here too (displayClass): its count lands in
	// displayTotals["clean"], and the raw "converged" bucket is left empty so
	// the loop below skips it via the ordinary n == 0 guard.
	displayTotals := map[string]int{}
	for cls, n := range model.Summary {
		displayTotals[displayClass(cls)] += n
	}
	var segs []string
	for _, cls := range classOrder {
		n := displayTotals[cls]
		if n == 0 {
			continue
		}
		_, color := styleClass(p, cls)
		segs = append(segs, color(fmt.Sprintf("%d %s", n, cls)))
	}
	// hasSummary gates both the summary line itself and the trailing --legend
	// hint below: an enabled agent that renders nothing prints only "(no
	// tracked items)" above, and a hint about classification words that never
	// appeared would be pointing at nothing.
	hasSummary := len(segs) > 0
	if hasSummary {
		fmt.Fprintln(p.Out, "")
		fmt.Fprintln(p.Out, strings.Join(segs, "  ·  "))
	}
	renderStatusLegend(p, model.Summary)
	if collapsed > 0 {
		fmt.Fprintln(p.Out, "")
		fmt.Fprintln(p.Out, p.Faint(fmt.Sprintf("%d skill %s collapsed; pass --verbose to list every bundled file.",
			collapsed, plural(collapsed, "directory", "directories"))))
	}
	if hasSummary {
		fmt.Fprintln(p.Out, "")
		fmt.Fprintln(p.Out, p.Faint("Run `agentsync status --legend` for a brief on each classification status."))
	}
}

// renderAgentItems prints one agent's tracked items. In verbose mode every item
// is a row; otherwise items under a common skill directory collapse into one
// summary row. Returns the number of skill directories that were collapsed (a
// single-file skill is printed as a normal row and not counted, since collapsing
// it would hide nothing).
func renderAgentItems(p *ui.Printer, items []statusItem, verbose bool) int {
	if verbose {
		for _, it := range items {
			renderStatusItem(p, it)
		}
		return 0
	}
	// Group skill-directory items by their root, preserving first-appearance
	// order; everything else (memory, subagents, commands, MCP/hook/LSP keys)
	// stays an inline per-item row.
	roots := skillRoots(items)
	type entry struct {
		item  statusItem
		group *skillGroup
	}
	groups := map[string]*skillGroup{}
	var order []entry
	for _, it := range items {
		root := ""
		if it.Pointer == "" {
			root = skillRootOf(it.Path, roots)
		}
		if root == "" {
			order = append(order, entry{item: it})
			continue
		}
		g := groups[root]
		if g == nil {
			g = &skillGroup{Root: root}
			groups[root] = g
			order = append(order, entry{group: g})
		}
		g.Items = append(g.Items, it)
	}
	collapsed := 0
	for _, e := range order {
		if e.group == nil {
			renderStatusItem(p, e.item)
			continue
		}
		if len(e.group.Items) == 1 {
			renderStatusItem(p, e.group.Items[0])
			continue
		}
		renderSkillGroup(p, e.group)
		collapsed++
	}
	return collapsed
}

// renderStatusItem prints one tracked file or merged key on a single row.
func renderStatusItem(p *ui.Printer, it statusItem) {
	disp := it.Path
	if it.Pointer != "" {
		disp = it.Path + "#" + it.Pointer
	}
	// Path/Pointer embed a config-derived component name/id (a skill/subagent/
	// command dirname, an mcp/lsp id) that source.Load reads verbatim, so a
	// control byte in a shared config's name reaches here — sanitize on display
	// (issue #93/#171).
	disp = ui.Sanitize(disp)
	glyph, color := styleClass(p, it.Class)
	// Pad the plain "glyph class" to a fixed visible width BEFORE
	// coloring so ANSI bytes never shift the path column. The printed word
	// is the display class (converged folds into clean); the color/glyph
	// lookup uses the raw class, though the two currently share a style.
	label := ui.Pad(glyph+" "+displayClass(it.Class), 20)
	fmt.Fprintf(p.Out, "  %s %s\n", color(label), disp)
}

// renderSkillGroup prints a collapsed skill directory: the headline class is the
// most severe among its files, and a faint suffix reports the file count (plus a
// per-class breakdown when the files don't all share one class).
func renderSkillGroup(p *ui.Printer, g *skillGroup) {
	cls := mostSevereClass(g.Items)
	glyph, color := styleClass(p, cls)
	label := ui.Pad(glyph+" "+displayClass(cls), 20)
	fmt.Fprintf(p.Out, "  %s %s  %s\n", color(label), ui.Sanitize(g.Root+string(filepath.Separator)), p.Faint(skillSummary(g.Items)))
}

// skillRoots returns the skill directories present among items, anchored on an
// actual SKILL.md — the dir of every `…/skills/<name>/SKILL.md` item (one whose
// grandparent dir is `skills`, the shape every adapter renders: Claude
// `~/.claude/skills`, the cross-vendor `~/.agents/skills`, each generic spec's
// own dir). Anchoring on a real SKILL.md rather than merely a `skills` path
// SEGMENT is deliberate: a user whose $HOME or project root happens to contain a
// `skills` ancestor (e.g. `/home/skills/user`) would otherwise see EVERY dest
// path — including a drifted memory/subagent file — swept into one bogus
// "skill" group and hidden from the per-row view. A skill whose SKILL.md is gone
// (a true orphan) simply isn't collapsed; its lingering files list individually,
// which is the clearer view for cleanup anyway.
//
// Candidates nested under another candidate are dropped: a skill that itself
// bundles a `skills/<sub>/SKILL.md` (a legal bundled file — the loader excludes
// only the top-level SKILL.md) would otherwise yield a second inner root, and
// the inner SKILL.md would match BOTH, leaving the grouping to map-iteration
// order. Keeping only outermost roots guarantees they never nest, so every path
// matches at most one root and the whole skill collapses onto one row.
func skillRoots(items []statusItem) map[string]bool {
	cand := map[string]bool{}
	for _, it := range items {
		if it.Pointer != "" || filepath.Base(it.Path) != "SKILL.md" {
			continue
		}
		dir := filepath.Dir(it.Path) // …/skills/<name>
		if filepath.Base(filepath.Dir(dir)) == "skills" {
			cand[dir] = true
		}
	}
	roots := map[string]bool{}
	for r := range cand {
		if !hasAncestorIn(r, cand) {
			roots[r] = true
		}
	}
	return roots
}

// hasAncestorIn reports whether some OTHER member of set is a parent-path of r.
func hasAncestorIn(r string, set map[string]bool) bool {
	for other := range set {
		if other != r && strings.HasPrefix(r, other+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// skillRootOf returns the skill root in roots that contains path (path is the
// root's SKILL.md or any descendant of it), or "" if none. skillRoots guarantees
// roots never nest, so at most one can match — the first match is unambiguous.
func skillRootOf(path string, roots map[string]bool) string {
	for r := range roots {
		if path == r || strings.HasPrefix(path, r+string(filepath.Separator)) {
			return r
		}
	}
	return ""
}

// mostSevereClass returns the highest-severity drift class present among items,
// so the collapsed headline never downplays a problem hiding among clean files.
func mostSevereClass(items []statusItem) string {
	present := map[string]bool{}
	for _, it := range items {
		present[it.Class] = true
	}
	for _, cls := range classSeverity {
		if present[cls] {
			return cls
		}
	}
	if len(items) > 0 {
		return items[0].Class // class outside the known set; show it verbatim
	}
	return ""
}

// skillSummary builds the faint parenthetical describing a collapsed skill: how
// many files it bundles (phrased relative to SKILL.md when present, matching how
// a skill is authored) and, when the files span more than one drift class, the
// per-class breakdown so a mixed directory isn't flattened to its headline alone.
func skillSummary(items []statusItem) string {
	total := len(items)
	hasSkillMD := false
	counts := map[string]int{}
	for _, it := range items {
		if filepath.Base(it.Path) == "SKILL.md" {
			hasSkillMD = true
		}
		// Bucket by display class so a skill mixing clean and converged
		// files reads as one clean group, not a spurious breakdown.
		counts[displayClass(it.Class)]++
	}
	var size string
	if hasSkillMD {
		extra := total - 1
		size = fmt.Sprintf("SKILL.md + %d %s", extra, plural(extra, "file", "files"))
	} else {
		size = fmt.Sprintf("%d %s", total, plural(total, "file", "files"))
	}
	if len(counts) <= 1 {
		return "(" + size + ")"
	}
	var segs []string
	for _, cls := range classOrder {
		if n := counts[cls]; n > 0 {
			segs = append(segs, fmt.Sprintf("%d %s", n, cls))
		}
	}
	return "(" + size + "; " + strings.Join(segs, ", ") + ")"
}

// plural returns one or many depending on n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// classLegend gives a one-line, action-focused explanation of what `apply`
// will do for an item in each drift class. Phrased so each line reads as a
// continuation of "apply will…". "clean" and "converged" are both
// intentionally omitted: the dashboard already displays converged as clean
// (displayClass), and "no action" for either would just be noise. The legend
// itself is also skipped entirely when the summary contains nothing but
// clean/converged items, so a fully-synced status report stays as terse as
// today. `status --legend` (renderClassLegend) reuses these SAME strings
// verbatim for the classes covered here, so the two views can never disagree
// about what apply does to a given class — an earlier draft kept a second,
// independently-worded copy for --legend and it drifted out of sync with this
// one on its first day (drift/conflict: "apply blocks" here vs. "will be
// overwritten" there — only the latter matches actual apply behavior, which
// overwrites with NO backup for drift/conflict specifically — the
// destination is already state-owned, so Writer.maybeBackupFileOp's
// foreign-collision backup path doesn't apply; see internal/render/writer.go).
// orphan-drifted's line is deliberately hedged rather than promising a
// backup outright: `apply` only reclaims (and therefore only backs up before
// deleting) destinations whose SourceID is a skill/subagent/command — see
// render.OrphanIsReclaimable. A drifted orphan OUTSIDE those kinds (e.g.
// memory) is currently still classified orphan-drifted by status's
// unfiltered render.OrphanFiles, but apply never touches its destination at
// all — no backup, no delete, just a dropped state entry — a pre-existing
// mismatch between classification and reclamation this PR doesn't fix.
var classLegend = map[string]string{
	"new":               "will be created",
	"pending":           "will be updated to match source",
	"drift":             "will be overwritten (use reconcile to keep the dest edit)",
	"conflict":          "will be overwritten (use reconcile to merge the dest edit)",
	"foreign-collision": "will be backed up and overwritten",
	"orphan":            "will be deleted",
	"orphan-drifted":    "will be backed up, then deleted, if apply still reclaims it (the local edit is lost either way)",
}

// renderStatusLegend prints a brief glossary of the drift classes that
// actually appear in the summary. Each line is colored to match the per-item
// dashboard above and prefixed with the same glyph, so the user can scan
// from a body row to its meaning by shape and color, not just by word.
func renderStatusLegend(p *ui.Printer, summary map[string]int) {
	type entry struct {
		cls, msg string
	}
	var rows []entry
	for _, cls := range classOrder {
		if summary[cls] == 0 {
			continue
		}
		msg, ok := classLegend[cls]
		if !ok {
			continue
		}
		rows = append(rows, entry{cls, msg})
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprintln(p.Out, "")
	fmt.Fprintln(p.Out, p.Faint("What `apply` will do:"))
	for _, r := range rows {
		glyph, color := styleClass(p, r.cls)
		label := ui.Pad(glyph+" "+r.cls, 20)
		fmt.Fprintf(p.Out, "  %s %s\n", color(label), p.Faint(r.msg))
	}
}

// classMeaning gives a one-line, plain statement of what each drift class
// MEANS — not what apply does about it, which stays classLegend's job (reused
// verbatim in renderClassLegend below) so the two can't say contradictory
// things about the same class. Every one of the nine classes drift.Classify
// can produce has an entry here; TestClassTablesCoverAllDriftClasses guards
// that against a tenth class silently falling through unlisted. Mirrors the
// class table in docs/concepts.md; keep the two in sync.
var classMeaning = map[string]string{
	"clean":             "source, last-applied state, and destination all agree",
	"pending":           "you changed the source since the last apply",
	"drift":             "the destination was edited outside agentsync",
	"converged":         "source and destination changed independently but landed on the same value",
	"conflict":          "source and destination changed to different values",
	"new":               "a brand-new item with nothing on disk yet",
	"foreign-collision": "a pre-existing file agentsync never wrote",
	"orphan":            "removed from source and untouched since",
	"orphan-drifted":    "removed from source, but the destination was also edited",
}

// renderClassLegend prints the full nine-class drift glossary for
// `status --legend` — a standalone reference independent of any project's
// current drift state, so it needs no plan/state/secrets loaded. classOrder
// already lists all nine classes, so it doubles as this table's row order;
// the action half of each line comes straight from classLegend (clean and
// converged, which classLegend omits as self-evident/folded, get their own
// text here instead).
func renderClassLegend(p *ui.Printer) {
	fmt.Fprintln(p.Out, p.Bold("Drift classification statuses"))
	for _, cls := range classOrder {
		var action string
		switch cls {
		case drift.Clean.String():
			action = "apply does nothing"
		case drift.Converged.String():
			action = "apply just refreshes state silently (a normal `status` run shows this as \"clean\", since there's nothing left to reconcile)"
		default:
			action = classLegend[cls]
		}
		msg := classMeaning[cls] + " — " + action
		glyph, color := styleClass(p, cls)
		label := ui.Pad(glyph+" "+cls, 20)
		fmt.Fprintf(p.Out, "  %s %s\n", color(label), p.Faint(msg))
	}
}

// emitStatusWarnings writes advisory diagnostics to stderr: orphaned state from
// a removed/disabled agent, and native plugins not yet declared in source.
// These are not part of the status model (and not the --json payload).
//
// orphan detection uses the full enabled set so a `--agents`-narrowed report
// never mistakes a deselected-but-enabled agent for an orphan; the
// undeclared-plugin nudge follows the selected set so it stays scoped to what
// the report shows.
func emitStatusWarnings(p *ui.Printer, c source.Canonical, reg *adapter.Registry, s *state.Targets, enabled, selected []string, sc adapter.Scope) {
	for _, a := range orphanedStateAgents(s, enabled) {
		p.Warnf("agent %q is not enabled but still owns tracked files/keys in state; its "+
			"native config is orphaned. Run `agentsync agent disable %q --purge` to remove what agentsync wrote.", a, a)
	}
	// Nudge: plugins installed natively in an enabled agent but not yet declared
	// in source. agentsync treats them as foreign-managed (never drift), so this
	// is informational — it points at `import`.
	undeclared := undeclaredNativePlugins(c, reg, selected)
	for _, name := range reg.Names() {
		missing := undeclared[name]
		if len(missing) == 0 {
			continue
		}
		// Native plugin names come from the agent's own config (a plugin author
		// can influence them); they are untrusted.Text and sanitize on display by
		// construction (untrusted.Join renders each via its String()), so no manual
		// ui.Sanitize is needed here. The agent `name` is a trusted registry id.
		p.Infof("%d plugin(s) installed in %s are not in your source (%s); "+
			"run `agentsync import %s:plugin` to manage them.",
			len(missing), name, untrusted.Join(missing, ", "), name)
	}
	// Warn: the reverse case — a plugin the agent installs ITSELF that agentsync
	// also projects there, so every component lands twice. apply never reads the
	// destination (its plan is a pure function of canonical state), so this is
	// the only place the collision can be noticed once the plugin is declared.
	// SCOPED, unlike the nudge above. project.Merge does not overlay Plugins, so
	// the merged canonical carries the USER pins while a project-scope render
	// honours the PROJECT ones — reading the wrong pin's `native_agents` here
	// warns about a duplicate the project does not have, and stays silent about
	// one it does. reportCanonical is the existing answer to exactly this
	// mismatch. (The undeclared nudge above is correctly unscoped: "is this
	// declared anywhere in my source" is a question about the merged view.)
	rc := reportCanonical(c, sc)
	duplicated := duplicatedNativePlugins(rc, reg, selected)
	warned := false
	for _, name := range reg.Names() {
		dupes := duplicated[name]
		if len(dupes) == 0 {
			continue
		}
		warned = true
		p.Warnf("%d plugin(s) are installed in %s AND projected there by agentsync (%s), "+
			"so their skills/subagents/commands land twice and their hooks fire twice. "+
			"Either uninstall them in %s, or add %q to `native_agents` in plugins/<id>.toml "+
			"to let %s keep serving them itself.",
			len(dupes), name, untrusted.Join(dupes, ", "), name, name, name)
	}
	// Following `selected` is correct — a narrowed report should not talk about
	// agents it excluded, exactly as the undeclared nudge above does not. But
	// silence then carries two meanings: "no duplicates" and "duplicates on an
	// agent you narrowed away". Say which, so an absent warning is never read as
	// a clean bill of health for agents this run never looked at.
	//
	// Emitted whether or not a warning fired: "I checked 1 of 3 agents and found
	// nothing" is the case most likely to be misread, and it is the case where
	// the loop above prints nothing at all.
	//
	// Both halves of the guard mirror the check itself rather than re-deriving
	// it. `rc` is the same canonical duplicatedNativePlugins was given (see the
	// scope comment above) and declaredPlugins applies the same !Disabled filter
	// it early-returns on, so the note cannot claim a check that never ran. And
	// only agents with a native plugin manager can duplicate anything, so an
	// agent without one going unexamined is not a gap worth reporting — naming
	// the agents beats a bare count, and lets the note fall silent when nothing
	// examinable was narrowed away.
	if unexamined := unexaminedPluginAgents(reg, enabled, selected); len(unexamined) > 0 && len(declaredPlugins(rc)) > 0 {
		// "reported above" rather than "found": duplicatedNativePlugins skips an
		// agent whose IngestPlugins probe errors, so an absent warning means
		// nothing was REPORTED, which is not the same as nothing being there.
		// Before this note the distinction did not surface — the failure mode was
		// silence — but an affirmative sentence would turn that silence into a
		// claim, which is the exact defect this note exists to remove.
		lead := "no duplicate was reported above, but this"
		if warned {
			lead = "this"
		}
		// Agent names are trusted registry ids (as in the warning above, where
		// only the plugin names needed untrusted.Join).
		n := len(unexamined)
		p.Infof("%s check covered only the agent(s) --agents selected; %s also %s plugins "+
			"natively and %s not examined for duplicate plugin projection.",
			lead, strings.Join(unexamined, ", "),
			plural(n, "installs", "install"), plural(n, "was", "were"))
	}
}

// unexaminedPluginAgents returns the enabled agents a `--agents`-narrowed run
// left out of the duplicate check AND that could actually have carried a
// duplicate — i.e. those whose adapter implements adapter.PluginIngester, the
// same capability duplicatedNativePlugins requires before it examines an agent.
//
// Filtering on it is what keeps the scoping note honest. An agent with no
// native plugin concept can never duplicate a plugin, so counting it would
// inflate the note into a warning about a risk that does not exist — and, worse,
// would make the note fire on a narrowing that hid nothing.
func unexaminedPluginAgents(reg *adapter.Registry, enabled, selected []string) []string {
	sel := make(map[string]bool, len(selected))
	for _, n := range selected {
		sel[n] = true
	}
	var out []string
	for _, n := range enabled {
		if sel[n] {
			continue
		}
		if _, ok := reg.Lookup(n).(adapter.PluginIngester); !ok {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// orphanedStateAgents returns, sorted, the agent names recorded in a state key
// that are not in the enabled set — i.e. agents whose rendered native config and
// state entries linger after a `remove` or a `disable` without `--purge`.
func orphanedStateAgents(s *state.Targets, enabled []string) []string {
	en := make(map[string]bool, len(enabled))
	for _, n := range enabled {
		en[n] = true
	}
	found := map[string]bool{}
	collect := func(agent string) {
		if agent != "" && !en[agent] {
			found[agent] = true
		}
	}
	for k := range s.Files {
		collect(k.Agent)
	}
	for k := range s.Keys {
		collect(k.Agent)
	}
	out := make([]string, 0, len(found))
	for a := range found {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// stateFileKey builds the state Files map key for a destination. It exists only
// to convert adapter.Scope to the plain string internal/state takes (state sits
// below adapter in the package layering); the format itself is owned by
// state.Key, so this can no longer drift from render.RecordOpsState.
func stateFileKey(userHome, agent string, sc adapter.Scope, projectRoot, path string) state.Key {
	return state.NewFileKey(userHome, agent, sc.String(), projectRoot, path)
}

// stateKeyKey builds the state Keys map key for one JSON pointer inside a
// shared destination file. Same conversion-only role as stateFileKey.
func stateKeyKey(userHome, agent string, sc adapter.Scope, projectRoot, path, ptr string) state.Key {
	return state.NewPointerKey(userHome, agent, sc.String(), projectRoot, path, ptr)
}

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hashFile returns the SHA-256 hex digest of the file at path. Returns
// the empty string on missing-file errors (which `drift.Classify` reads
// as "absent" — the expected signal for Orphan / OrphanDrifted).
//
// If the path is a symlink, hashFile returns a special marker so the
// drift classifier can flag the file as drifted in a way the user can
// act on. A managed file becoming a symlink (e.g. user replaced
// .claude.json with `ln -s /dev/null`) used to silently read through
// the link and compare hashes — making the swap invisible to status.
func hashFile(path string) string {
	info, lerr := os.Lstat(path)
	if lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		// Return a sentinel that will never match a content hash.
		// We don't include the link target to keep the sentinel stable
		// (target may resolve to whatever attacker chose); just signal
		// "this is a symlink now."
		return "symlink-not-regular-file"
	}
	// A FIFO, device, or socket at a destination path would make os.ReadFile
	// BLOCK forever rather than fail — wedging `status`, which is advertised as
	// read-only, and reconcile's orphan listing. None has a content hash worth
	// computing, so answer a sentinel that can never match one. It is a DIFFERENT
	// sentinel from the symlink case above so a diagnostic never calls a FIFO a
	// symlink; both are opaque to callers, which only ever compare hashes for
	// equality. Shares render's predicate so the destination-read guards cannot
	// disagree about what is safe to read.
	if !render.IsRegularOrAbsent(path) {
		return "not-a-regular-file"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return hashContent(data)
}

// modeDrifted reports whether the regular file at path exists with permission
// bits that differ from the mode agentsync last recorded for it (state
// FileEntry.Mode). A recorded mode of 0 means "unspecified" (older state, or an
// op whose adapter left Mode unset — the writer defaults those to 0o644 on
// write), so it never counts as drift. A missing, symlinked, or non-regular file
// is left to the content classifier (which already flags it), so this returns
// false there. It is the permission-bit analog of the content-hash drift the
// classifier detects: a content-identical chmod is real drift the next apply
// re-converges (render.Writer.Write).
func modeDrifted(recordedMode uint32, path string) bool {
	if recordedMode == 0 {
		return false
	}
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return false
	}
	return fi.Mode().Perm() != os.FileMode(recordedMode).Perm()
}

func hashAnyValue(v any) string {
	if v == nil {
		return ""
	}
	data, _ := json.Marshal(v)
	return hashContent(data)
}

// standardizeJSONC converts JSONC (comments, trailing commas) to plain JSON
// bytes so encoding/json can parse it. It mirrors how the adapters read these
// destination files (hujson.Parse + Standardize), keeping the drift/import
// read paths in agreement with the apply write path.
func standardizeJSONC(data []byte) ([]byte, error) {
	v, err := hujson.Parse(data)
	if err != nil {
		return nil, err
	}
	v.Standardize()
	return v.Pack(), nil
}

func getPointerValue(m map[string]any, ptr string) any {
	if !strings.HasPrefix(ptr, "/") {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	var cur any = m
	for _, p := range parts {
		// Decode RFC 6901 escapes so a managed id containing '~' or '/'
		// (which CollectPointers escaped to ~0/~1) matches the real key.
		// Without this, status/diff looked up the literal escaped key, found
		// nothing, and reported phantom drift forever for that item.
		p = strings.ReplaceAll(p, "~1", "/")
		p = strings.ReplaceAll(p, "~0", "~")
		mp, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mp[p]
	}
	return cur
}
