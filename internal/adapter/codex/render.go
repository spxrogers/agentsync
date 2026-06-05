package codex

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
)

// Render converts the resolved canonical into FileOps for Codex CLI.
//
// Render projects each plugin's COMPONENTS (MCP, hooks, memory, skills,
// subagents, commands) to Codex's native paths and intentionally does NOT
// re-emit `[plugins."<name>@<source>"]` enable-state into ~/.codex/config.toml
// — `IngestPlugins` reads those tables on `import` for discovery, but they
// are NEVER written back on apply. This is the same cross-adapter invariant
// the Claude adapter follows (`PluginIngester` is read-only by design); see
// `internal/adapter/adapter.go` and `docs/architecture.md` § "PluginIngester
// (read-only)" for the full rationale. Foreign `[plugins.*]` entries the user
// set in Codex's UI are preserved by config.toml's merge-toml-keys writer
// because this render claims no keys under that section.
func (a *Adapter) Render(r secrets.Resolved, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	c := r.Canonical() //nolint:forbidigo // sanctioned render egress: project the resolved model into FileOps (never written back to source)
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)

	// At project scope render only the project-overlay items so user-scope
	// items are not duplicated into the project directory.
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
	if cmdOps, cmdSkips, err := a.renderCommands(renderC, p, scope); err != nil {
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
	// LSP: Codex has no LSP concept — skip with explanation.
	for _, l := range renderC.LSPServers {
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
