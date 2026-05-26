package codex

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
)

// Render converts the resolved canonical into FileOps for Codex CLI.
func (a *Adapter) Render(r secrets.Resolved, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	c := r.Canonical() //nolint:forbidigo // sanctioned render egress: project the resolved model into FileOps (never written back to source)
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var ops []adapter.FileOp
	var skips []adapter.Skip

	if mcpOps, err := a.renderMCP(c, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, mcpOps...)
	}
	if memOps, err := a.renderMemory(c, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, memOps...)
	}
	if skOps, err := a.renderSkills(c, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, skOps...)
	}
	if saOps, saSkips, err := a.renderSubagents(c, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, saOps...)
		skips = append(skips, saSkips...)
	}
	if cmdOps, cmdSkips, err := a.renderCommands(c, p, scope); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, cmdOps...)
		skips = append(skips, cmdSkips...)
	}
	if hookOps, hookSkips, err := a.renderHooks(c, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, hookOps...)
		skips = append(skips, hookSkips...)
	}
	// LSP: Codex has no LSP concept — skip with explanation.
	for _, l := range c.LSPServers {
		skips = append(skips, adapter.Skip{
			Component: "lsp", Name: l.ID,
			Reason: "Codex has no LSP configuration concept",
		})
	}
	return ops, skips, nil
}

// agentTargeted reports whether the agents allowlist includes codex. An
// empty/nil list or a "*" entry means all agents are targeted.
func agentTargeted(name string, agents []string) bool {
	if len(agents) == 0 {
		return true
	}
	for _, a := range agents {
		if a == "*" || a == name {
			return true
		}
	}
	return false
}
