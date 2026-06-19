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

func newStatusCmd() *cobra.Command {
	var (
		scopeFlag   string
		projectFlag string
		jsonOut     bool
		agentFlag   []string
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
			home := paths.AgentsyncHome(paths.OSEnv{})
			// Load WITH the plugin cache so drift classification sees the
			// same plugin-projected components `apply` writes; source.Load
			// alone would report plugin-managed files/keys as untracked.
			userHome := paths.HomeDir(paths.OSEnv{})
			c, sc, projectRoot, err := loadProjectedForScope(cmd, afero.NewOsFs(), home, scopeFlag, projectFlag, true)
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
			// --agent narrows the report (and the plan) to the requested
			// agent(s); orphan-state warnings still consider the FULL enabled
			// set so a deselected agent isn't mistaken for an orphaned one.
			selected := enabledAgents
			if len(agentFlag) > 0 {
				sel, serr := resolveAgentFilter(agentFlag, enabled)
				if serr != nil {
					return serr
				}
				selected = sel
			}
			if len(selected) == 0 {
				if jsonOut {
					emitStatusWarnings(p, c, reg, s, enabledAgents, selected)
					return emitJSON(p.Out, statusModel{Agents: []statusAgent{}, Summary: map[string]int{}})
				}
				fmt.Fprintln(p.Out, "no agents enabled; run `agentsync agent add claude` (or opencode)")
				emitStatusWarnings(p, c, reg, s, enabledAgents, selected)
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
				emitStatusWarnings(p, c, reg, s, enabledAgents, selected)
				return emitJSON(p.Out, model)
			}
			renderStatusText(p, model, statusVerbose(cmd))
			emitStatusWarnings(p, c, reg, s, enabledAgents, selected)
			return nil
		},
	}
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: user; prompts when run inside a project tree)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON instead of the formatted report")
	cmd.Flags().StringSliceVar(&agentFlag, "agent", nil, "limit the report to specific agent(s) (repeatable or comma-separated)")
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

// resolveAgentFilter validates the --agent values against the known agent set
// and the currently-enabled set, returning the de-duplicated selection in the
// order given. An unknown name or an agent that exists but is not enabled is a
// hard error — silently dropping it would make `status --agent typo` look clean.
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
	return out, nil
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
			hsrc := hashContent(op.Content)
			happlied := s.Files[stateFileKey(userHome, name, sc, projectRoot, op.Path)].SHA256
			hdest := hashFile(op.Path)
			cls := drift.Classify(hsrc, happlied, hdest).String()
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

// classOrder is the stable display order for the summary footer and groups the
// drift classes by severity for coloring.
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

// styleClass maps a drift class to its glyph and color. Green = synced, cyan =
// a pending change apply will make, red = unexpected/destructive drift, yellow
// = an orphan needing a decision.
func styleClass(p *ui.Printer, cls string) (glyph string, color func(string) string) {
	switch cls {
	case "clean", "converged":
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
		collapsed += renderAgentItems(p, ag.Items, verbose)
	}

	// Summary footer lists only non-zero classes, so the words "drift" /
	// "conflict" / "pending" never appear when there is none of that state.
	var segs []string
	for _, cls := range classOrder {
		n := model.Summary[cls]
		if n == 0 {
			continue
		}
		_, color := styleClass(p, cls)
		segs = append(segs, color(fmt.Sprintf("%d %s", n, cls)))
	}
	if len(segs) > 0 {
		fmt.Fprintln(p.Out, "")
		fmt.Fprintln(p.Out, strings.Join(segs, "  ·  "))
	}
	renderStatusLegend(p, model.Summary)
	if collapsed > 0 {
		fmt.Fprintln(p.Out, "")
		fmt.Fprintln(p.Out, p.Faint(fmt.Sprintf("%d skill %s collapsed; pass --verbose to list every bundled file.",
			collapsed, plural(collapsed, "directory", "directories"))))
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
	type entry struct {
		item  statusItem
		group *skillGroup
	}
	groups := map[string]*skillGroup{}
	var order []entry
	for _, it := range items {
		root := ""
		if it.Pointer == "" {
			if r, ok := skillRoot(it.Path); ok {
				root = r
			}
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
	glyph, color := styleClass(p, it.Class)
	// Pad the plain "glyph class" to a fixed visible width BEFORE
	// coloring so ANSI bytes never shift the path column.
	label := ui.Pad(glyph+" "+it.Class, 20)
	fmt.Fprintf(p.Out, "  %s %s\n", color(label), disp)
}

// renderSkillGroup prints a collapsed skill directory: the headline class is the
// most severe among its files, and a faint suffix reports the file count (plus a
// per-class breakdown when the files don't all share one class).
func renderSkillGroup(p *ui.Printer, g *skillGroup) {
	cls := mostSevereClass(g.Items)
	glyph, color := styleClass(p, cls)
	label := ui.Pad(glyph+" "+cls, 20)
	fmt.Fprintf(p.Out, "  %s %s  %s\n", color(label), g.Root+string(filepath.Separator), p.Faint(skillSummary(g.Items)))
}

// skillRoot returns the …/skills/<name> directory a path lives under and true if
// the path is inside one. Every adapter renders skills to a `skills/<name>/`
// subtree (Claude `~/.claude/skills`, the cross-vendor `~/.agents/skills`, each
// generic spec's own dir), so the first `skills` path segment plus the next
// segment is the skill root for any of them. A nested `upstream/SKILL.md` falls
// under the same outer root, keeping a multi-variant skill on one row.
func skillRoot(path string) (string, bool) {
	sep := string(filepath.Separator)
	parts := strings.Split(path, sep)
	for i, seg := range parts {
		if seg == "skills" && i+1 < len(parts) {
			return strings.Join(parts[:i+2], sep), true
		}
	}
	return "", false
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
		counts[it.Class]++
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
// continuation of "apply will…". "clean" is intentionally omitted: the word
// is self-evident and adding "no action" would just be noise; the legend
// itself is also skipped entirely when the summary contains nothing but
// "clean" items, so a fully-synced status report stays as terse as today.
var classLegend = map[string]string{
	"converged":         "no action (source and dest now match)",
	"new":               "will be created",
	"pending":           "will be updated to match source",
	"drift":             "will be overwritten (use reconcile to keep the dest edit)",
	"conflict":          "will be overwritten (use reconcile to merge the dest edit)",
	"foreign-collision": "will be backed up and overwritten",
	"orphan":            "will be deleted",
	"orphan-drifted":    "will be deleted (a local edit will be lost)",
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

// emitStatusWarnings writes advisory diagnostics to stderr: orphaned state from
// a removed/disabled agent, and native plugins not yet declared in source.
// These are not part of the status model (and not the --json payload).
//
// orphan detection uses the full enabled set so a `--agent`-narrowed report
// never mistakes a deselected-but-enabled agent for an orphan; the
// undeclared-plugin nudge follows the selected set so it stays scoped to what
// the report shows.
func emitStatusWarnings(p *ui.Printer, c source.Canonical, reg *adapter.Registry, s *state.Targets, enabled, selected []string) {
	for _, a := range orphanedStateAgents(s, enabled) {
		fmt.Fprintf(p.Err, "%s agent %q is not enabled but still owns tracked files/keys in state; its "+
			"native config is orphaned. Run `agentsync agent disable %s --purge` to remove what agentsync wrote.\n",
			p.Yellow("warning:"), a, a)
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
		fmt.Fprintf(p.Err, "%s %d plugin(s) installed in %s are not in your source (%s); "+
			"run `agentsync import %s:plugin` to manage them.\n",
			p.Cyan("note:"), len(missing), name, untrusted.Join(missing, ", "), name)
	}
}

// orphanedStateAgents returns, sorted, the agent names that appear as a state
// key prefix ("agent:scope:…") but are not in the enabled set — i.e. agents
// whose rendered native config and state entries linger after a `remove` or a
// `disable` without `--purge`.
func orphanedStateAgents(s *state.Targets, enabled []string) []string {
	en := make(map[string]bool, len(enabled))
	for _, n := range enabled {
		en[n] = true
	}
	found := map[string]bool{}
	collect := func(key string) {
		if i := strings.IndexByte(key, ':'); i > 0 {
			if a := key[:i]; !en[a] {
				found[a] = true
			}
		}
	}
	for k := range s.Files {
		collect(k)
	}
	for k := range s.Keys {
		collect(k)
	}
	out := make([]string, 0, len(found))
	for a := range found {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// stateFileKey builds the state Files map key matching render.RecordOpsState.
// Format: "agent:scope:portableProject:portablePath" (project and path are
// HOME-relative against the user's $HOME so keys are portable across
// machines — userHome must be paths.HomeDir, NOT the agentsync home).
func stateFileKey(userHome, agent string, sc adapter.Scope, projectRoot, path string) string {
	return fmt.Sprintf("%s:%s:%s:%s", agent, sc.String(),
		paths.HomeRelative(userHome, projectRoot), paths.HomeRelative(userHome, path))
}

// stateKeyKey builds the state Keys map key matching render.RecordOpsState.
// Format: "agent:scope:portableProject:portablePath:ptr".
func stateKeyKey(userHome, agent string, sc adapter.Scope, projectRoot, path, ptr string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s", agent, sc.String(),
		paths.HomeRelative(userHome, projectRoot), paths.HomeRelative(userHome, path), ptr)
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
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return hashContent(data)
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
