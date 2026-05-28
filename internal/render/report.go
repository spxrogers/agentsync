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
	// Disabled is true when the plugin is disabled for this scope (e.g. by a
	// project marker's [plugins] disabled). Its components are not rendered.
	Disabled bool `json:"disabled,omitempty"`
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
// RenderPlan.  It associates ops/skips with plugins by matching MCP server IDs
// contributed by each plugin.
//
// For each plugin P and each agent A:
//   - count MCP servers that appear in plan.PerAgent[A].Ops whose SourceID
//     matches one of P's components.
//   - coverage = full if skips==0 && mcp>0 (or no components), partial if
//     skips>0 && mcp>0, none if mcp==0.
//
// Plugins with no canonical entries (not yet cached) still generate rows with
// coverage=none so the operator can see what's missing.
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
				Plugin:   "(base)",
				Agent:    agName,
				MCP:      countMCPServers(c, agName),
				Commands: len(c.Commands),
				Skips:    len(res.Skips),
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
			// For now we attribute all ops/skips equally to each plugin row
			// since the canonical model is flattened.  A future version can
			// tag ops with their origin plugin.
			row := PluginRow{
				Plugin:   label,
				Agent:    agName,
				MCP:      countMCPServers(c, agName),
				Commands: len(c.Commands),
				Skips:    len(res.Skips),
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
