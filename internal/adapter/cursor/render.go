package cursor

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
)

// Render converts the resolved canonical into FileOps for Cursor.
//
// Render projects each plugin's COMPONENTS (MCP, memory, skills, subagents,
// commands, hooks) to Cursor's native `.cursor/` paths. Cursor has a native
// plugin system, but where it records local enable-state is undocumented, so
// the adapter implements no PluginIngester yet and never re-emits any
// enable-state on apply — the same read-is-discovery / apply-fans-out-components
// invariant every adapter follows (see docs/architecture.md § "PluginIngester
// (read-only)").
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
	if memOps, memSkips, err := a.renderMemory(renderC, p, scope); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, memOps...)
		skips = append(skips, memSkips...)
	}
	if skOps, err := a.renderSkills(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, skOps...)
	}
	if saOps, saSkips, err := a.renderSubagents(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, saOps...)
		skips = append(skips, saSkips...)
	}
	if cmdOps, cmdSkips, err := a.renderCommands(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, cmdOps...)
		skips = append(skips, cmdSkips...)
	}
	if hookOps, hookSkips, err := a.renderHooks(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, hookOps...)
		skips = append(skips, hookSkips...)
	}
	// LSP: Cursor has no LSP configuration concept — skip with explanation.
	for _, l := range renderC.LSPServers {
		skips = append(skips, adapter.Skip{
			Component: "lsp", Name: l.ID,
			Reason: "Cursor has no LSP configuration concept",
		})
	}
	return ops, skips, nil
}
