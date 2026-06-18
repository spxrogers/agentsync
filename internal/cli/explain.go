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
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
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
			// Read-only: a strict plugin.json/entry conflict degrades to a
			// warning + entry-wins so explain still shows coverage.
			c, err := marketplace.LoadProjectedLenient(afero.NewOsFs(), home, pluginCacheRoot, nil)
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

			return runExplain(cmd.OutOrStdout(), p, c, wanted, home, jsonOut)
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
		if pl.Plugin.ID != "" {
			byID[pl.Plugin.ID] = pl
		}
		if pl.ID != "" {
			byID[pl.ID] = pl
		}
	}
	seen := map[string]bool{}
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
				ID:       explainLabel(pl),
				Version:  pl.Plugin.Version,
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
	maxLabel := 0
	for _, pl := range plugins {
		if n := visibleLen(explainLabel(pl)); n > maxLabel {
			maxLabel = n
		}
	}
	for _, pl := range plugins {
		label := explainLabel(pl)
		hasTrail := pl.Plugin.Version != "" || pl.Plugin.Disabled
		// Only pad the label when something follows it — a bare row with
		// trailing spaces just leaves visible whitespace at end-of-line.
		shown := label
		if hasTrail {
			shown = ui.Pad(label, maxLabel)
		}
		line := fmt.Sprintf("  %s %s", p.Cyan(ui.GlyphInfo), shown)
		if pl.Plugin.Version != "" {
			line += "  " + p.Faint("v"+pl.Plugin.Version)
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
func runExplain(w io.Writer, p *ui.Printer, c source.Canonical, wanted []source.Plugin, home string, jsonOut bool) error {
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
	statePath := filepath.Join(home, ".state", "targets.json")
	s, err := state.Load(statePath)
	if err != nil {
		return err
	}

	// One shared plan over the full canonical: every plugin-row in the
	// report already reads from the same per-agent op set today (BuildReport
	// attributes counts globally — see its doc-comment). Planning per
	// plugin would not change a row's numbers and would multiply work.
	plan, err := render.Plan(secrets.ForRender(c), reg, agents, adapter.ScopeUser, "", s, paths.HomeDir(paths.OSEnv{}))
	if err != nil {
		return err
	}

	if jsonOut {
		// Build one combined report whose rows cover exactly the requested
		// plugins, in the order resolveExplainTargets returned them.
		filtered := c
		filtered.Plugins = wanted
		report := render.BuildReport(filtered, plan, agents)
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

		filtered := c
		filtered.Plugins = []source.Plugin{pl}
		report := render.BuildReport(filtered, plan, agents)
		// The report already groups by plugin and renders one row per
		// agent — emit it stripped of its own "plugin: …" header to avoid
		// duplicating the one we just printed.
		emitReportBody(w, p, report)
	}
	return nil
}

// emitPluginHeader prints a styled "▸ <id>  v<version>  (disabled)" line.
func emitPluginHeader(w io.Writer, p *ui.Printer, pl source.Plugin) {
	label := explainLabel(pl)
	parts := []string{p.Bold(p.Cyan("▸") + " " + label)}
	if pl.Plugin.Version != "" {
		parts = append(parts, p.Faint("v"+pl.Plugin.Version))
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
		tail := p.Faint(fmt.Sprintf("%d mcp · %d commands", row.MCP, row.Commands))
		if row.Skips > 0 {
			tail += "  " + p.Yellow(fmt.Sprintf("(%d skipped)", row.Skips))
		}
		fmt.Fprintf(w, "  %s %s  %s  %s\n",
			p.Faint(ui.GlyphArrow),
			p.Bold(ui.Pad(row.Agent, 10)),
			mark,
			tail)
		// "(N skipped)" is a dead end on its own — list each skipped component
		// (what it is, and why the agent could not translate it) beneath the row.
		emitSkipDetails(w, p, row.SkipDetails)
	}
}

// emitSkipDetails lists each skipped component under its agent row as a faint,
// indented "<component> <name>  <reason>" line so the "(N skipped)" tally is
// explained rather than opaque. Reasons are aligned by padding the
// component+name column to its widest entry.
func emitSkipDetails(w io.Writer, p *ui.Printer, skips []render.SkipDetail) {
	if len(skips) == 0 {
		return
	}
	width := 0
	for _, s := range skips {
		if n := visibleLen(skipLabel(s)); n > width {
			width = n
		}
	}
	for _, s := range skips {
		fmt.Fprintf(w, "      %s %s  %s\n",
			p.Yellow(ui.GlyphInfo),
			ui.Pad(skipLabel(s), width),
			p.Faint(s.Reason))
	}
}

// skipLabel renders "<component> <name>" for a skipped item, or just the
// component when the skip has no name (e.g. an unrecognized hook event).
func skipLabel(s render.SkipDetail) string {
	if s.Name == "" {
		return s.Component
	}
	return s.Component + " " + s.Name
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
// falling back to the short id).
func explainLabel(pl source.Plugin) string {
	if pl.Plugin.ID != "" {
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
