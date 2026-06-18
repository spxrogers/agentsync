// Package render — translation report.
//
// TranslationReport summarises, per plugin (or source component), how many
// items each adapter rendered vs skipped. Emitted at the end of apply (and
// rendered per-plugin by explain); verify does not print it.
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
	// MCP, Commands, Skills, Subagents, Hooks, and LSP count the components the
	// plugin (or whole model) hosts that target this agent — the inventory, so
	// the report is descriptive of everything the plugin ships, not just MCP +
	// commands. MCP and LSP honour each server's `enabled`/`agents` targeting
	// (a server scoped to other agents is not counted here); the markdown
	// component kinds (commands/skills/subagents) and hooks have no per-agent
	// allowlist, so their counts are the model's totals. These describe what is
	// hosted, NOT what successfully rendered: a hosted component the adapter
	// cannot translate is still counted here and reported under Skips below.
	MCP       int `json:"mcp"`
	Commands  int `json:"commands"`
	Skills    int `json:"skills"`
	Subagents int `json:"subagents"`
	Hooks     int `json:"hooks"`
	LSP       int `json:"lsp"`
	// Skips is the number of components the adapter explicitly skipped.
	Skips int `json:"skips"`
	// SkipDetails enumerates the components behind Skips — what the adapter
	// could not translate, and why — so a "(N skipped)" tally is never a dead
	// end. Attribution follows the canonical+plan passed to BuildReport (see its
	// doc): when the caller passes the whole flattened model (apply), the
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

// printText is the shared body of PrintText / PrintTextStyled. The only
// untrusted token it renders is the plugin label (a fetched marketplace id),
// which is passed through ui.Sanitize before reaching the terminal so a hostile
// plugin cannot smuggle ANSI/control sequences into the report; see the loop.
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
		// The plugin label is the plugin id from fetched marketplace metadata
		// (untrusted), so sanitize it before rendering to the terminal: a control
		// sequence smuggled into a plugin id must not recolor/clear the screen or
		// spoof rows in the translation report `apply` prints. Sanitizing strips
		// embedded newlines too, so the id cannot forge an extra report line.
		// Clean labels pass through unchanged, so the byte-stable plain fixtures
		// still hold. (This text path is the one `apply` prints; `explain` renders
		// its own report body and sanitizes the same untrusted source there.)
		//
		// Grouping and ordering still key on the RAW label (byPlugin,
		// pluginOrder, sort.Strings): two distinct raw ids that sanitize to the
		// same visible text therefore render as separate rows that merely look
		// alike — a cosmetic spoof of the same class as the bidi/zero-width runes
		// ui.Sanitize deliberately leaves, not a terminal-control escape, so it
		// is an accepted residual.
		label := ui.Sanitize(plug)
		if p != nil {
			fmt.Fprintf(w, "%s %s\n", p.Bold("plugin:"), label)
		} else {
			fmt.Fprintf(w, "plugin: %s\n", label)
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
// RenderPlan: one row per plugin (c.Plugins) × agent. Each row carries the
// per-agent component inventory (countMCPServers / countLSPServers honour
// enabled+agent targeting; commands/skills/subagents/hooks are model totals) and
// the skips from plan.PerAgent[agent].Skips.
//
// coverage = full when skips==0; otherwise partial when the adapter still
// rendered something for the agent (plan ops non-empty), none when every hosted
// component was skipped. The rendered signal is the plan's ops, not the inventory
// counts — a hosted-but-unsupported component (e.g. an LSP server on an agent
// with no LSP concept) is counted in the inventory yet renders nothing, so its
// row is "none". A disabled plugin yields a single disabled-marker row. With no
// plugins installed, one "(base)" row per agent summarizes the whole canonical.
//
// BuildReport does NOT itself correlate a component to its origin plugin — the
// projected canonical is flattened with no origin tag. Attribution is therefore
// the caller's choice of what model+plan to pass:
//   - apply passes the whole flattened model, so every plugin row carries
//     the same global counts/skips (the documented summary behavior).
//   - `explain <id>` re-projects ONE plugin in isolation
//     (marketplace.ProjectInstalled) and passes a model+plan holding only that
//     plugin's components, so its row reflects that plugin alone.
func BuildReport(c source.Canonical, plan RenderPlan, agents []string) TranslationReport {
	var report TranslationReport

	// The canonical is flattened (no origin-plugin tag), so the per-agent counts
	// are computed over whatever model the caller scoped: apply passes the
	// whole model (one global summary repeated per plugin row), while `explain
	// <id>` passes a model holding only one re-projected plugin's components.
	//
	// With no plugins installed we emit one "(base)" row per agent over the whole
	// canonical; otherwise one row per plugin × agent.

	if len(c.Plugins) == 0 {
		for _, agName := range agents {
			res, ok := plan.PerAgent[agName]
			if !ok {
				continue
			}
			row := reportRow("(base)", agName, c, res)
			report.Rows = append(report.Rows, row)
		}
		return report
	}

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
			report.Rows = append(report.Rows, reportRow(label, agName, c, res))
		}
	}
	return report
}

// reportRow builds one plugin×agent row: the per-agent component inventory
// (counts of what the model hosts for agName), the skip tally + details, and the
// derived coverage. c is whatever model the caller scoped (whole model or one
// plugin); res is that agent's render result.
func reportRow(label, agName string, c source.Canonical, res AgentResult) PluginRow {
	row := PluginRow{
		Plugin:      label,
		Agent:       agName,
		MCP:         countMCPServers(c, agName),
		LSP:         countLSPServers(c, agName),
		Commands:    len(c.Commands),
		Skills:      len(c.Skills),
		Subagents:   len(c.Subagents),
		Hooks:       len(c.Hooks),
		Skips:       len(res.Skips),
		SkipDetails: skipDetails(res.Skips),
	}
	row.Coverage = computeCoverage(row, len(res.Ops) > 0)
	return row
}

// computeCoverage derives the coverage string. full = nothing skipped. Otherwise
// partial when the adapter still rendered something for this agent (rendered),
// none when every hosted component was skipped (or there was nothing to render).
// The rendered signal comes from the plan's ops — NOT from the inventory counts,
// which include hosted-but-skipped components (e.g. an LSP server on an agent with
// no LSP concept is counted in row.LSP yet renders nothing, so its row is "none").
func computeCoverage(row PluginRow, rendered bool) string {
	if row.Skips == 0 {
		return "full"
	}
	if rendered {
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

// countLSPServers mirrors countMCPServers for LSP servers — LSPServerSpec carries
// the same source-only enabled/agents targeting fields, so a server disabled or
// scoped to other agents is not counted for this agent.
func countLSPServers(c source.Canonical, agent string) int {
	n := 0
	for _, l := range c.LSPServers {
		if l.Spec.Enabled != nil && !*l.Spec.Enabled {
			continue
		}
		if targetsAgent(l.Spec.Agents, agent) {
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
