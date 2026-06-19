package roo

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
)

// Render converts the resolved canonical into FileOps for Roo Code.
//
// MCP renders at project scope only (.roo/mcp.json); Roo's global MCP is VS Code
// globalStorage, which agentsync intentionally does not target (the prior-art
// consensus), so user-scope MCP is reported as a skip. Memory and commands render
// at BOTH scopes (~/.roo and <repo>/.roo). Roo has no native plugin enable-state
// agentsync models, so there is no PluginIngester; it still receives
// plugin-projected components on apply.
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
	if memOps, err := a.renderMemory(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, memOps...)
	}
	if cmdOps, cmdSkips, err := a.renderCommands(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, cmdOps...)
		skips = append(skips, cmdSkips...)
	}

	// Components Roo has no faithful target for — skipped with a report.
	for _, s := range renderC.Skills {
		skips = append(skips, adapter.Skip{Component: "skill", Name: s.Name, Reason: "Roo has no Agent Skills concept", Kind: adapter.SkipDropped})
	}
	for _, s := range renderC.Subagents {
		skips = append(skips, adapter.Skip{Component: "subagent", Name: s.Name, Reason: "Roo uses custom modes, not per-file subagents", Kind: adapter.SkipDropped})
	}
	for _, h := range renderC.Hooks {
		skips = append(skips, adapter.Skip{Component: "hook", Name: h.Event, Reason: "Roo has no declarative hook concept", Kind: adapter.SkipDropped})
	}
	for _, l := range renderC.LSPServers {
		skips = append(skips, adapter.Skip{Component: "lsp", Name: l.ID, Reason: "Roo has no LSP configuration concept", Kind: adapter.SkipDropped})
	}
	return ops, skips, nil
}
