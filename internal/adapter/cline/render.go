package cline

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
)

// Render converts the resolved canonical into FileOps for Cline.
//
// Scope-asymmetric: MCP renders at user scope (the Cline CLI's ~/.cline/mcp.json;
// Cline has no project MCP file), while memory + commands render at project scope
// (.clinerules/; Cline's global rules are a non-XDG app path agentsync does not
// target). The non-applicable scope reports a skip for each affected item. Cline
// has no native plugin enable-state agentsync models, so there is no
// PluginIngester; it still receives plugin-projected components on apply.
func (a *Adapter) Render(r secrets.Resolved, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, nil, err
	}
	c := r.Canonical() //nolint:forbidigo // sanctioned render egress: project the resolved model into FileOps (never written back to source)
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)

	// At project scope render only the project-overlay items so user-scope items
	// are not duplicated into the project directory.
	renderC := c
	if scope == adapter.ScopeProject && c.Project != nil {
		renderC = *c.Project
	}

	var ops []adapter.FileOp
	var skips []adapter.Skip

	if mcpOps, mcpSkips, err := a.renderMCP(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, mcpOps...)
		skips = append(skips, mcpSkips...)
	}
	if memOps, memSkips, err := a.renderMemory(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, memOps...)
		skips = append(skips, memSkips...)
	}
	if cmdOps, cmdSkips, err := a.renderCommands(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, cmdOps...)
		skips = append(skips, cmdSkips...)
	}

	// Components Cline has no faithful target for — skipped with a report.
	for _, s := range renderC.Skills {
		skips = append(skips, adapter.Skip{Component: "skill", Name: s.Name, Reason: "Cline has no Agent Skills concept"})
	}
	for _, s := range renderC.Subagents {
		skips = append(skips, adapter.Skip{Component: "subagent", Name: s.Name, Reason: "Cline has no subagent concept"})
	}
	for _, h := range renderC.Hooks {
		skips = append(skips, adapter.Skip{Component: "hook", Name: h.Event, Reason: "Cline has no declarative hook concept"})
	}
	for _, l := range renderC.LSPServers {
		skips = append(skips, adapter.Skip{Component: "lsp", Name: l.ID, Reason: "Cline has no LSP configuration concept"})
	}
	return ops, skips, nil
}
