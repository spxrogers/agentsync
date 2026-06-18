// Package render — translation report.
//
// TranslationReport summarises, per plugin (or source component), how many
// items each adapter rendered vs skipped. Emitted at the end of apply/verify.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
)

// PluginRow is one row in the translation report — one plugin × one agent.
type PluginRow struct {
	Plugin string `json:"plugin"`
	Agent  string `json:"agent"`
	// Coverage is "full", "partial", or "none".
	Coverage string `json:"coverage"`
	// MCP is the number of MCP servers rendered for this plugin×agent pair.
	MCP int `json:"mcp"`
	// Commands is the number of slash commands rendered.
	Commands int `json:"commands"`
	// Skips is the number of components the adapter explicitly skipped.
	Skips int `json:"skips"`
	// SkipDetails enumerates the components behind Skips — what the adapter
	// could not translate, and why — so a "(N skipped)" tally is never a dead
	// end. Attribution follows the canonical+plan passed to BuildReport (see its
	// doc): when the caller passes the whole flattened model (apply/verify), the
	// skips are the agent's across every component and are repeated under each
	// plugin row; when the caller passes a per-plugin-scoped model (`explain
	// <id>`, which re-projects one plugin in isolation), they are narrowed to
	// that plugin's own components.
	SkipDetails []SkipDetail `json:"skipDetails,omitempty"`
	// Disabled is true when the plugin is disabled for this scope (e.g. by a
	// project marker's [plugins] disabled). Its components are not rendered.
	Disabled bool `json:"disabled,omitempty"`
}

// SkipDetail names one component an adapter could not translate, with the
// human reason it was skipped. It mirrors adapter.Skip but carries JSON tags
// for the report's machine-readable surface.
type SkipDetail struct {
	Component string `json:"component"`
	Name      string `json:"name,omitempty"`
	Reason    string `json:"reason"`
}

// skipDetails converts an adapter's []Skip into the report's JSON-tagged form.
func skipDetails(skips []adapter.Skip) []SkipDetail {
	if len(skips) == 0 {
		return nil
	}
	out := make([]SkipDetail, len(skips))
	for i, s := range skips {
		out[i] = SkipDetail{Component: s.Component, Name: s.Name, Reason: s.Reason}
	}
	return out
}

// TranslationReport holds all plugin×agent rows.
type TranslationReport struct {
	Rows []PluginRow `json:"rows"`
}

// coverageMark converts a Coverage string to its display symbol.
func coverageMark(cov string) string {
	switch cov {
	case "full":
		return "✓ full  "
	case "partial":
		return "◐ partial"
	default:
		return "✗ none  "
	}
}

// PrintText writes the human-readable report to w in its plain form.
//
//	plugin: demo@test-mp
//	  claude    ✓ full   (1 mcp, 0 commands)
//	  opencode  ✓ full   (1 mcp, 0 commands)
//
// Use PrintTextStyled to render the same report with semantic color via a
// *ui.Printer. Plain output is byte-stable so existing test fixtures hold.
func (r TranslationReport) PrintText(w io.Writer) {
	r.printText(w, nil)
}

// PrintTextStyled writes the report with the same layout as PrintText, but
// styled via p: bold "plugin:" labels, semantically colored coverage marks
// (green=full, yellow=partial, red=none), and faint trailing counts. When p
// has color disabled (non-TTY, NO_COLOR, --color=never), the output is
// visually identical to PrintText — only ANSI is suppressed.
func (r TranslationReport) PrintTextStyled(w io.Writer, p *ui.Printer) {
	r.printText(w, p)
}

func (r TranslationReport) printText(w io.Writer, p *ui.Printer) {
	// Group rows by plugin.
	byPlugin := map[string][]PluginRow{}
	pluginOrder := []string{}
	seen := map[string]bool{}
	for _, row := range r.Rows {
		if !seen[row.Plugin] {
			pluginOrder = append(pluginOrder, row.Plugin)
			seen[row.Plugin] = true
		}
		byPlugin[row.Plugin] = append(byPlugin[row.Plugin], row)
	}
	sort.Strings(pluginOrder)

	for _, plug := range pluginOrder {
		if p != nil {
			fmt.Fprintf(w, "%s %s\n", p.Bold("plugin:"), plug)
		} else {
			fmt.Fprintf(w, "plugin: %s\n", plug)
		}
		rows := byPlugin[plug]
		sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
		for _, row := range rows {
			if row.Disabled {
				if p != nil {
					fmt.Fprintf(w, "  %s\n", p.Faint("(disabled by project)"))
				} else {
					fmt.Fprintf(w, "  (disabled by project)\n")
				}
				continue
			}
			mark := coverageMark(row.Coverage)
			tail := fmt.Sprintf("(%d mcp, %d commands)", row.MCP, row.Commands)
			if p != nil {
				mark = colorCoverage(p, row.Coverage, mark)
				tail = p.Faint(tail)
			}
			fmt.Fprintf(w, "  %-10s %s %s\n", row.Agent, mark, tail)
		}
	}
}

// colorCoverage maps a coverage string to its semantic color. The mark itself
// (with trailing alignment padding) is colored as one unit, so a colored space
// run doesn't shift the column that follows.
func colorCoverage(p *ui.Printer, cov, mark string) string {
	switch cov {
	case "full":
		return p.Green(mark)
	case "partial":
		return p.Yellow(mark)
	default:
		return p.Red(mark)
	}
}

// PrintJSON writes the structured JSON form.
func (r TranslationReport) PrintJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// BuildReport constructs a TranslationReport from the canonical model and a
// RenderPlan: one row per plugin (c.Plugins) × agent. Each row's counts come
// from the model — MCP servers that target the agent (countMCPServers) and
// len(c.Commands) — and its skips from plan.PerAgent[agent].Skips.
//
// coverage = full when skips==0, partial when skips>0 but something rendered
// (mcp>0 || commands>0), none otherwise. A disabled plugin yields a single
// disabled-marker row. With no plugins installed, one "(base)" row per agent
// summarizes the whole canonical.
//
// BuildReport does NOT itself correlate a component to its origin plugin — the
// projected canonical is flattened with no origin tag. Attribution is therefore
// the caller's choice of what model+plan to pass:
//   - apply/verify pass the whole flattened model, so every plugin row carries
//     the same global counts/skips (the documented summary behavior).
//   - `explain <id>` re-projects ONE plugin in isolation
//     (marketplace.ProjectInstalled) and passes a model+plan holding only that
//     plugin's components, so its row reflects that plugin alone.
func BuildReport(c source.Canonical, plan RenderPlan, agents []string) TranslationReport {
	var report TranslationReport

	// Index each plugin by the MCP server IDs it contributes.
	// We use Plugins from the canonical model; the MCP server IDs are the keys
	// we expect to see in the rendered ops.
	//
	// Because plugin projection injects MCPServers with the same IDs as in
	// plugin.json, we can match ops by SourceID prefix or by looking at the
	// op's content for the server key.  The simplest approach: for each plugin
	// record a label and count how many MCP servers with that label appeared.

	// Build per-plugin MCP server name sets from the canonical MCPServers.
	// We can't directly correlate which MCPServer came from which plugin here
	// (projection flattens them); instead we show aggregate counts per agent
	// for each installed plugin as a summary row.
	//
	// The report is per-installed-plugin.  If there are no plugins, we emit a
	// summary row per agent with the global count.

	if len(c.Plugins) == 0 {
		// No plugins installed — emit one row per agent for the global canonical.
		for _, agName := range agents {
			res, ok := plan.PerAgent[agName]
			if !ok {
				continue
			}
			row := PluginRow{
				Plugin:      "(base)",
				Agent:       agName,
				MCP:         countMCPServers(c, agName),
				Commands:    len(c.Commands),
				Skips:       len(res.Skips),
				SkipDetails: skipDetails(res.Skips),
			}
			row.Coverage = computeCoverage(row)
			report.Rows = append(report.Rows, row)
		}
		return report
	}

	// One row per plugin × agent.
	for _, plug := range c.Plugins {
		label := plug.Plugin.ID
		if label == "" {
			label = plug.ID
		}
		// A disabled plugin (e.g. project-marker [plugins] disabled) contributes
		// no rendered components — emit a single row marking it disabled rather
		// than per-agent rows with misleading global counts.
		if plug.Plugin.Disabled {
			report.Rows = append(report.Rows, PluginRow{
				Plugin:   label,
				Coverage: "disabled",
				Disabled: true,
			})
			continue
		}
		for _, agName := range agents {
			res, ok := plan.PerAgent[agName]
			if !ok {
				continue
			}
			// Counts/skips are attributed per the canonical+plan passed in (see
			// the doc-comment). apply/verify pass the whole flattened model, so
			// every plugin row carries the same global numbers; `explain <id>`
			// passes a model+plan scoped to one re-projected plugin, so its row
			// reflects only that plugin's components.
			row := PluginRow{
				Plugin:      label,
				Agent:       agName,
				MCP:         countMCPServers(c, agName),
				Commands:    len(c.Commands),
				Skips:       len(res.Skips),
				SkipDetails: skipDetails(res.Skips),
			}
			row.Coverage = computeCoverage(row)
			report.Rows = append(report.Rows, row)
		}
	}
	return report
}

// computeCoverage derives the coverage string from a PluginRow's counts.
func computeCoverage(row PluginRow) string {
	if row.Skips == 0 {
		return "full"
	}
	if row.MCP > 0 || row.Commands > 0 {
		return "partial"
	}
	return "none"
}

// countMCPServers counts the canonical MCP servers that render for agent —
// the actual server count, not the op count. The previous countMCPOps counted
// merge-json-keys ops, which is always 1 for claude's single .claude.json
// merge regardless of how many servers it holds, and wrongly also counted
// hooks/lspServers ops (same strategy) as MCP.
func countMCPServers(c source.Canonical, agent string) int {
	n := 0
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if targetsAgent(m.Server.Agents, agent) {
			n++
		}
	}
	return n
}

// targetsAgent reports whether an Agents allowlist includes agent. An empty
// list or one containing "*" targets every agent.
func targetsAgent(agents []string, agent string) bool {
	if len(agents) == 0 {
		return true
	}
	for _, a := range agents {
		if a == "*" || a == agent {
			return true
		}
	}
	return false
}
