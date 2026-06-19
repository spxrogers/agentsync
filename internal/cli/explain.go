package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

func newExplainCmd() *cobra.Command {
	var (
		jsonOut bool
		list    bool
		all     bool
	)
	cmd := &cobra.Command{
		Use:   "explain [<plugin-id>...]",
		Short: "show per-agent translation for one or more plugins",
		Long: "Show per-agent translation coverage for installed plugins.\n\n" +
			"Pass one or more plugin ids (space-separated) to explain just those,\n" +
			"--all to explain every installed plugin, or --list to print the set of\n" +
			"installed plugin ids without rendering coverage.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if list && all {
				return fmt.Errorf("--list and --all are mutually exclusive")
			}
			if list && len(args) > 0 {
				return fmt.Errorf("--list does not take plugin ids")
			}
			if all && len(args) > 0 {
				return fmt.Errorf("--all does not take plugin ids; it explains every installed plugin")
			}
			if !list && !all && len(args) == 0 {
				return fmt.Errorf("explain needs at least one plugin id, --all, or --list")
			}

			home := paths.AgentsyncHome(paths.OSEnv{})
			pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
			fs := afero.NewOsFs()
			// Read-only: a strict plugin.json/entry conflict degrades to a
			// warning + entry-wins so explain still shows coverage.
			c, err := marketplace.LoadProjectedLenient(fs, home, pluginCacheRoot, nil)
			if err != nil {
				return err
			}

			p, err := newPrinter(cmd)
			if err != nil {
				return err
			}

			if list {
				return runExplainList(p, c, jsonOut)
			}

			// Resolve the requested plugin ids (--all expands to every installed
			// plugin id; otherwise we keep argv order so the user sees rows in
			// the order they asked for).
			wanted, missing := resolveExplainTargets(c, args, all)
			if len(missing) > 0 {
				return fmt.Errorf("plugin(s) not installed: %s; run 'agentsync plugin list' to see installed plugins",
					strings.Join(missing, ", "))
			}
			if len(wanted) == 0 {
				// --all with no installed plugins is an honest empty state.
				return runExplainEmpty(p, jsonOut)
			}

			return runExplain(cmd.OutOrStdout(), p, fs, c, wanted, home, pluginCacheRoot, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "structured JSON output")
	cmd.Flags().BoolVar(&list, "list", false, "list installed plugin ids and exit")
	cmd.Flags().BoolVar(&all, "all", false, "explain every installed plugin")
	return cmd
}

// resolveExplainTargets matches user-supplied plugin ids against the canonical
// model's installed plugins. With all=true the args are ignored and every
// installed plugin is returned in id order. Missing ids are reported separately
// so the caller can emit one combined error rather than one per typo.
func resolveExplainTargets(c source.Canonical, args []string, all bool) (matched []source.Plugin, missing []string) {
	if all {
		matched = append(matched, c.Plugins...)
		sort.Slice(matched, func(i, j int) bool { return explainLabel(matched[i]) < explainLabel(matched[j]) })
		return matched, nil
	}
	// Index installed plugins by both labels they may be referenced as
	// (the @marketplace form and the short id).
	byID := map[string]source.Plugin{}
	for _, pl := range c.Plugins {
		if !pl.Plugin.ID.Empty() {
			byID[pl.Plugin.ID.Unverified()] = pl
		}
		if !pl.ID.Empty() {
			byID[pl.ID.Unverified()] = pl
		}
	}
	seen := map[untrusted.Text]bool{}
	for _, want := range args {
		pl, ok := byID[want]
		if !ok {
			missing = append(missing, want)
			continue
		}
		key := explainLabel(pl)
		if seen[key] {
			continue
		}
		seen[key] = true
		matched = append(matched, pl)
	}
	return matched, missing
}

// runExplainList prints the set of installed plugin ids so users don't have to
// jump to `plugin list` just to remember what they can explain.
func runExplainList(p *ui.Printer, c source.Canonical, jsonOut bool) error {
	type listRow struct {
		ID       string `json:"id"`
		Version  string `json:"version,omitempty"`
		Disabled bool   `json:"disabled,omitempty"`
	}
	plugins := make([]source.Plugin, len(c.Plugins))
	copy(plugins, c.Plugins)
	sort.Slice(plugins, func(i, j int) bool { return explainLabel(plugins[i]) < explainLabel(plugins[j]) })

	if jsonOut {
		rows := make([]listRow, 0, len(plugins))
		for _, pl := range plugins {
			rows = append(rows, listRow{
				// --json is the machine contract: emit the RAW id/version, the
				// consumer owns escaping (Unverified bypasses display sanitization).
				ID:       explainLabel(pl).Unverified(),
				Version:  pl.Plugin.Version.Unverified(),
				Disabled: pl.Plugin.Disabled,
			})
		}
		return emitJSON(p.Out, struct {
			Plugins []listRow `json:"plugins"`
		}{Plugins: rows})
	}

	if len(plugins) == 0 {
		fmt.Fprintf(p.Out, "%s\n", p.Faint("no plugins installed — try `agentsync plugin install <id@marketplace>`"))
		return nil
	}

	fmt.Fprintf(p.Out, "%s %s\n", p.Bold("Installed plugins"),
		p.Faint(fmt.Sprintf("(%d)", len(plugins))))
	// Plugin id + version come from fetched marketplace metadata (untrusted), so
	// sanitize before width/Pad and display to keep terminal escapes out of the
	// text listing. (The --json branch above leaves them raw — that's the machine
	// contract, where the consumer owns escaping.)
	maxLabel := 0
	for _, pl := range plugins {
		if n := visibleLen(explainLabel(pl).String()); n > maxLabel {
			maxLabel = n
		}
	}
	for _, pl := range plugins {
		// explainLabel/Version are untrusted.Text; String() sanitizes for display.
		label := explainLabel(pl).String()
		hasTrail := !pl.Plugin.Version.Empty() || pl.Plugin.Disabled
		// Only pad the label when something follows it — a bare row with
		// trailing spaces just leaves visible whitespace at end-of-line.
		shown := label
		if hasTrail {
			shown = ui.Pad(label, maxLabel)
		}
		line := fmt.Sprintf("  %s %s", p.Cyan(ui.GlyphInfo), shown)
		if !pl.Plugin.Version.Empty() {
			line += "  " + p.Faint("v"+pl.Plugin.Version.String())
		}
		if pl.Plugin.Disabled {
			line += "  " + p.Yellow("(disabled)")
		}
		fmt.Fprintln(p.Out, line)
	}
	fmt.Fprintln(p.Out, "")
	fmt.Fprintf(p.Out, "%s %s\n",
		p.Faint(ui.GlyphArrow),
		p.Faint("explain coverage: `agentsync explain <plugin>` or `--all`"))
	return nil
}

// runExplainEmpty handles `--all` when no plugins are installed.
func runExplainEmpty(p *ui.Printer, jsonOut bool) error {
	if jsonOut {
		return emitJSON(p.Out, render.TranslationReport{})
	}
	fmt.Fprintf(p.Out, "%s\n", p.Faint("no plugins installed — try `agentsync plugin install <id@marketplace>`"))
	return nil
}

// runExplain renders translation coverage for the matched plugins. Each plugin
// is reported under a styled header (id, version, disabled marker); the per-
// agent rows reuse the same PrintTextStyled body apply uses, so the visual
// vocabulary stays consistent.
//
// Crucially, each plugin's coverage is computed from THAT plugin's own projected
// components, not from the flattened union of every installed plugin. The
// projected canonical concatenates all plugins' components with no origin tag, so
// a report built from it would stamp the same global MCP/command counts and skips
// onto every plugin row — `explain notion` would show another plugin's skipped
// subagents/LSPs. We re-project each requested plugin in isolation
// (marketplace.ProjectInstalled) and build its plan from only those components.
func runExplain(w io.Writer, p *ui.Printer, fs afero.Fs, c source.Canonical, wanted []source.Plugin, home, pluginCacheRoot string, jsonOut bool) error {
	// Collect enabled agents. Sort so `--json` row order is deterministic
	// (PrintJSON emits rows in this slice order verbatim).
	var agents []string
	for name, ag := range c.Config.Agents {
		if ag.Enabled {
			agents = append(agents, name)
		}
	}
	sort.Strings(agents)

	reg := registryFactory()

	if jsonOut {
		// One combined report whose rows cover exactly the requested plugins,
		// in the order resolveExplainTargets returned them — each plugin's rows
		// computed from its own components.
		var report render.TranslationReport
		for _, pl := range wanted {
			pr, err := explainPluginReport(fs, c, pl, agents, reg, home, pluginCacheRoot)
			if err != nil {
				return err
			}
			report.Rows = append(report.Rows, pr.Rows...)
		}
		return report.PrintJSON(w)
	}

	// Text path: render plugin-by-plugin so each gets its own styled section
	// header (with version + disabled marker), separated by a blank line.
	fmt.Fprintf(w, "%s %s\n",
		p.Bold("agentsync explain"),
		p.Faint(fmt.Sprintf("— translation coverage for %s", pluralize(len(wanted), "plugin"))))
	fmt.Fprintln(w, "")

	for i, pl := range wanted {
		if i > 0 {
			fmt.Fprintln(w, "")
		}
		emitPluginHeader(w, p, pl)

		report, err := explainPluginReport(fs, c, pl, agents, reg, home, pluginCacheRoot)
		if err != nil {
			return err
		}
		// The report already groups by plugin and renders one row per
		// agent — emit it stripped of its own "plugin: …" header to avoid
		// duplicating the one we just printed.
		emitReportBody(w, p, report)
	}
	return nil
}

// explainPluginReport builds the translation report for a SINGLE plugin, scoped
// to that plugin's own components. It re-projects the plugin in isolation and
// plans over a canonical carrying only those components, so the resulting
// coverage/skip rows reflect what this plugin contributes — never the flattened
// union of every installed plugin. The base config (enabled agents, settings) is
// preserved from c; only the component slices and the Plugins list are narrowed.
//
// A disabled plugin contributes nothing: BuildReport emits its single
// disabled-marker row from c.Plugins alone, so no projection or plan is needed.
//
// State is deliberately NOT threaded into render.Plan here (nil): explain is
// read-only, and a nil state makes the per-agent ops exactly the adapter's
// rendered ops, with no apply-time OwnedKeys/orphan-cleanup synthesis. BuildReport
// derives the partial-vs-none coverage from whether anything rendered
// (len(ops) > 0), so a stray orphan-cleanup op (one would be synthesized for every
// key a prior apply owns but this single-plugin scope no longer renders) must not
// be allowed to masquerade as a real render and flip a fully-skipped plugin's
// "none" to "partial".
func explainPluginReport(fs afero.Fs, c source.Canonical, pl source.Plugin, agents []string, reg *adapter.Registry, home, pluginCacheRoot string) (render.TranslationReport, error) {
	scoped := c
	scoped.Plugins = []source.Plugin{pl}
	if pl.Plugin.Disabled {
		return render.BuildReport(scoped, render.RenderPlan{}, agents), nil
	}

	// The ok flag is intentionally ignored: ProjectInstalled returns ok=false
	// only when projection is skipped wholesale (empty pluginCacheRoot, which
	// explain never passes) or for a disabled plugin (already handled above). In
	// any such case proj is the zero value, so the assignments below correctly
	// yield an empty-component report rather than the stale flattened union.
	proj, _, err := marketplace.ProjectInstalled(fs, home, pluginCacheRoot, pl, true)
	if err != nil {
		return render.TranslationReport{}, err
	}
	// Replace (not append to) the flattened component lists with only this
	// plugin's, so the plan and the report's counts cover this plugin alone.
	scoped.MCPServers = proj.MCPServers
	scoped.Skills = proj.Skills
	scoped.Subagents = proj.Subagents
	scoped.Commands = proj.Commands
	scoped.Hooks = proj.Hooks
	scoped.LSPServers = proj.LSPServers

	plan, err := render.Plan(secrets.ForRender(scoped), reg, agents, adapter.ScopeUser, "", nil, paths.HomeDir(paths.OSEnv{}))
	if err != nil {
		return render.TranslationReport{}, err
	}
	return render.BuildReport(scoped, plan, agents), nil
}

// emitPluginHeader prints a styled "▸ <id>  v<version>  (disabled)" line. The
// id and version come from fetched marketplace metadata (untrusted), so they are
// sanitized before styling to keep terminal escapes out of the header.
func emitPluginHeader(w io.Writer, p *ui.Printer, pl source.Plugin) {
	label := explainLabel(pl).String()
	parts := []string{p.Bold(p.Cyan("▸") + " " + label)}
	if !pl.Plugin.Version.Empty() {
		parts = append(parts, p.Faint("v"+pl.Plugin.Version.String()))
	}
	if pl.Plugin.Disabled {
		parts = append(parts, p.Yellow("(disabled)"))
	}
	fmt.Fprintln(w, strings.Join(parts, "  "))
}

// emitReportBody emits the agent rows for a single-plugin report, without the
// "plugin: …" header PrintTextStyled normally prints (we already drew our own
// section header above it).
func emitReportBody(w io.Writer, p *ui.Printer, r render.TranslationReport) {
	// Stable agent ordering matches PrintTextStyled.
	rows := append([]render.PluginRow(nil), r.Rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
	if len(rows) == 0 {
		fmt.Fprintf(w, "  %s\n", p.Faint("(no enabled agents — `agentsync agent add <name>`)"))
		return
	}
	for _, row := range rows {
		if row.Disabled {
			// BuildReport emits a single disabled-marker row (no per-agent
			// counts) for disabled plugins; the section header above already
			// shows "(disabled)", so this row would just duplicate that.
			continue
		}
		glyph, color := coverageGlyphAndColor(p, row.Coverage)
		// Column order matches apply's translation report: agent name (bold
		// padded), then the colored "<glyph> <coverage>" mark (padded plain
		// before coloring so ANSI never shifts the count column), then a
		// faint count tail with an optional skip note.
		mark := color(ui.Pad(glyph+" "+row.Coverage, 12))
		tail := p.Faint(componentInventory(row))
		if note := skipTailNote(p, row.SkipDetails); note != "" {
			tail += "  " + note
		}
		fmt.Fprintf(w, "  %s %s  %s  %s\n",
			p.Faint(ui.GlyphArrow),
			p.Bold(ui.Pad(row.Agent, 10)),
			mark,
			tail)
		// The tally is a dead end on its own — list each part the agent could
		// not fully translate (what it is, whether it was reduced or dropped,
		// and why) beneath the row.
		emitSkipDetails(w, p, row.Agent, row.SkipDetails)
	}
}

// componentInventory renders the faint count tail describing what the plugin
// hosts for this agent — every component kind, not just MCP + commands, so a
// plugin that ships (say) only an LSP server or only skills is no longer reported
// as a bare "0 mcp · 0 commands". Only non-zero kinds are listed, in a stable
// order, joined by " · "; a plugin that contributes nothing to this agent reads
// "no components". The counts describe the inventory; the coverage glyph and the
// "(N reduced · M dropped)" note describe what the agent could do with it.
func componentInventory(row render.PluginRow) string {
	parts := make([]string, 0, 6)
	// one/many give per-count labels; the abbreviations mcp/lsp are invariant.
	for _, c := range []struct {
		n         int
		one, many string
	}{
		{row.MCP, "mcp", "mcp"},
		{row.Commands, "command", "commands"},
		{row.Skills, "skill", "skills"},
		{row.Subagents, "subagent", "subagents"},
		{row.Hooks, "hook", "hooks"},
		{row.LSP, "lsp", "lsp"},
	} {
		if c.n <= 0 {
			continue
		}
		label := c.many
		if c.n == 1 {
			label = c.one
		}
		parts = append(parts, fmt.Sprintf("%d %s", c.n, label))
	}
	if len(parts) == 0 {
		return "no components"
	}
	return strings.Join(parts, " · ")
}

// emitSkipDetails lists the parts the agent could not fully translate beneath
// its row. A bare "(N skipped)" tally reads as if N whole components were thrown
// away — so each line is tagged with WHAT happened: "reduced" (the component
// still rendered, just without some fields the agent has no home for) or
// "dropped" (the whole component had no native target and was not emitted at
// all). A one-line header frames the list and defines the two verbs.
func emitSkipDetails(w io.Writer, p *ui.Printer, agent string, skips []render.SkipDetail) {
	if len(skips) == 0 {
		return
	}
	fmt.Fprintf(w, "      %s\n", p.Faint(fmt.Sprintf(
		"%s %s couldn't fully translate — reduced = rendered without some fields; dropped = not emitted:",
		ui.GlyphArrow, agent,
	)))
	// A skip's Name is untrusted.Text and skipLabel sanitizes it (via String())
	// while joining it to the adapter-fixed component kind, so labels are already
	// terminal-safe here — and sanitized BEFORE width/Pad so a stripped byte can't
	// skew the column. Reason (below) is adapter-authored (trusted) but still run
	// through ui.Sanitize defensively.
	labels := make([]string, len(skips))
	width := 0
	for i, s := range skips {
		labels[i] = skipLabel(s)
		if n := visibleLen(labels[i]); n > width {
			width = n
		}
	}
	for i, s := range skips {
		// Pad the status word (plain) to the widest real kind BEFORE coloring, so
		// the reason column holds even for the guard-impossible "unset" fallback and
		// ANSI never shifts it.
		word, color := skipStatus(p, s.Kind)
		fmt.Fprintf(w, "        %s %s  %s  %s\n",
			p.Faint(ui.GlyphInfo),
			ui.Pad(labels[i], width),
			color(ui.Pad(word, len("dropped"))),
			p.Faint(ui.Sanitize(s.Reason)))
	}
}

// skipStatus maps a skip's Kind to its display word + semantic color. An unset
// Kind — which the static (TestEverySkipLiteralSetsKind) and runtime
// (TestEveryAdapterClassifiesSkips) guards make unconstructable from real
// adapters — renders as a red "unset" (its String()), a bug made VISIBLE rather
// than silently mislabeled "dropped".
func skipStatus(p *ui.Printer, k adapter.SkipKind) (string, func(string) string) {
	switch k {
	case adapter.SkipReduced:
		return "reduced", p.Cyan
	case adapter.SkipDropped:
		return "dropped", p.Yellow
	default:
		return k.String(), p.Red
	}
}

// skipTailNote summarizes a row's skips as a compact "(R reduced · D dropped)"
// note (omitting a zero side), or "" when there are no skips. Splitting the
// count by kind keeps the inline tally from reading as "N whole components
// discarded" — most "skips" on a partial row are reductions, not drops. A skip
// that is neither (an unset Kind the guards forbid) is counted under its own
// "unset" bucket rather than silently folded into "dropped".
func skipTailNote(p *ui.Printer, skips []render.SkipDetail) string {
	var reduced, dropped, unset int
	for _, s := range skips {
		switch s.Kind {
		case adapter.SkipReduced:
			reduced++
		case adapter.SkipDropped:
			dropped++
		default:
			unset++
		}
	}
	var parts []string
	if reduced > 0 {
		parts = append(parts, fmt.Sprintf("%d reduced", reduced))
	}
	if dropped > 0 {
		parts = append(parts, fmt.Sprintf("%d dropped", dropped))
	}
	if unset > 0 {
		parts = append(parts, fmt.Sprintf("%d unset", unset))
	}
	if len(parts) == 0 {
		return ""
	}
	return p.Yellow("(" + strings.Join(parts, " · ") + ")")
}

// skipLabel renders "<component> <name>" for a skipped item, or just the
// component kind when the skip has no name (e.g. an unrecognized hook event).
// Component is now the plain kind (mcp, subagent, command, …); the reduced-vs-
// dropped distinction is carried by SkipDetail.Kind and shown as the status tag,
// not encoded in the component string.
func skipLabel(s render.SkipDetail) string {
	if s.Name.Empty() {
		return s.Component
	}
	// s.Name is untrusted.Text; String() sanitizes it for the display label.
	return s.Component + " " + s.Name.String()
}

// coverageGlyphAndColor maps a coverage string to a glyph + semantic color
// helper (matches the existing translation-report vocabulary).
func coverageGlyphAndColor(p *ui.Printer, cov string) (string, func(string) string) {
	switch cov {
	case "full":
		return ui.GlyphOK, p.Green
	case "partial":
		return ui.GlyphPartial, p.Yellow
	default:
		return ui.GlyphErr, p.Red
	}
}

// explainLabel returns the human label for a plugin (the @marketplace form,
// falling back to the short id). Both candidates are untrusted.Text, so the
// label stays untrusted: printed via %s / String() it sanitizes itself.
func explainLabel(pl source.Plugin) untrusted.Text {
	if !pl.Plugin.ID.Empty() {
		return pl.Plugin.ID
	}
	return pl.ID
}

// pluralize renders "1 plugin" / "2 plugins" — the trivial English-y form is
// enough for CLI surface and beats pulling in a dependency.
func pluralize(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// visibleLen counts runes (the labels here are ASCII plugin ids; this keeps it
// cheap and avoids a runewidth dependency for the unlikely non-ASCII case).
func visibleLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
