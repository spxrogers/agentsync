package gemini

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
)

// Render converts the resolved canonical into FileOps for Gemini CLI.
//
// Render projects each plugin's COMPONENTS (MCP, memory, commands, subagents,
// hooks) to Gemini's native `.gemini/` paths. Gemini CLI has no native plugin
// enable-state concept agentsync models (it uses extensions), so there is no
// PluginIngester and nothing is ever written back — the same components-only-on-
// apply rule every adapter follows.
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

	if mcpOps, err := a.renderMCP(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, mcpOps...)
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
	if saOps, saSkips, err := a.renderSubagents(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, saOps...)
		skips = append(skips, saSkips...)
	}
	if hookOps, hookSkips, err := a.renderHooks(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, hookOps...)
		skips = append(skips, hookSkips...)
	}
	// Skills: Gemini CLI has no Agent Skills concept (it uses extensions) — skip.
	for _, s := range renderC.Skills {
		skips = append(skips, adapter.Skip{
			Component: "skill", Name: s.Name,
			Reason: "Gemini CLI has no Agent Skills concept (use a Gemini extension)",
			Kind:   adapter.SkipDropped,
		})
	}
	// LSP: Gemini CLI has no LSP configuration concept — skip.
	for _, l := range renderC.LSPServers {
		skips = append(skips, adapter.Skip{
			Component: "lsp", Name: l.ID,
			Reason: "Gemini CLI has no LSP configuration concept",
			Kind:   adapter.SkipDropped,
		})
	}
	return ops, skips, nil
}
