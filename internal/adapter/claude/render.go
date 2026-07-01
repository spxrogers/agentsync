package claude

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// Render produces the full set of FileOps for a given resolved canonical model.
// Pure function: returns the same output for the same input (disk reads are
// treated as fixed inputs for the purposes of the merge-json-keys strategy).
func (a *Adapter) Render(r secrets.Resolved, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, nil, err
	}
	c := r.Canonical() //nolint:forbidigo // sanctioned render egress: project the resolved model into FileOps (never written back to source)
	paths := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)

	// At project scope render only the project-overlay items; the merged
	// canonical includes user-scope items that Claude Code already reads from
	// ~/.claude/ and must not be duplicated into <project>/.claude/.
	renderC := c
	if scope == adapter.ScopeProject && c.Project != nil {
		renderC = *c.Project
	}

	var ops []adapter.FileOp
	var skips []adapter.Skip

	// 1. MCP -> ~/.claude.json (user) or <proj>/.mcp.json (project)
	if mcpOps, err := a.renderMCP(renderC, paths, scope); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, mcpOps...)
	}

	// 2. Memory -> CLAUDE.md
	if memOps, err := a.renderMemory(renderC, paths); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, memOps...)
	}

	// 3. Skills -> ~/.claude/skills/<name>/SKILL.md
	if skillOps, err := a.renderSkills(renderC, paths); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, skillOps...)
	}

	// 4. Subagents -> ~/.claude/agents/<name>.md
	if subagentOps, err := a.renderSubagents(renderC, paths); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, subagentOps...)
	}

	// 5. Commands -> ~/.claude/commands/<name>.md
	if cmdOps, err := a.renderCommands(renderC, paths); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, cmdOps...)
	}

	// 6. Hooks -> settings.json /hooks/<event>
	if hookOps, err := a.renderHooks(renderC, paths); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, hookOps...)
	}

	// 7. LSP servers: Claude Code only reads LSP servers from plugin
	// manifests, not settings.json. agentsync does not synthesize Claude
	// plugins in v1, so canonical LSP servers are reported as skipped instead
	// of writing a settings key Claude silently ignores.
	for _, l := range renderC.LSPServers {
		if l.Spec.Enabled != nil && !*l.Spec.Enabled {
			continue
		}
		if !agentTargeted("claude", l.Spec.Agents) {
			continue
		}
		skips = append(skips, adapter.Skip{
			Component: "lsp", Name: l.ID,
			Reason: "Claude Code reads LSP servers only from plugin manifests; agentsync does not synthesize Claude plugins in v1",
			Kind:   adapter.SkipDropped,
		})
	}

	// Plugin enablement (settings.json#/enabledPlugins) and the marketplace
	// registry (settings.json#/extraKnownMarketplaces) are deliberately NOT
	// rendered. agentsync projects each plugin's components (skills, MCP,
	// subagents, commands, hooks, memory) to Claude Code's NATIVE paths
	// (~/.claude/skills/<name>/, mcpServers in .claude.json, etc.), where
	// Claude reads them as regular components — plugin attribution is
	// agentsync's internal grouping and Claude doesn't need to know. Writing
	// these back would (a) ping-pong against the user's /plugin disable in
	// Claude Code's own UI on the next apply, (b) duplicate components Claude
	// already serves from its install dir (~/.claude/plugins/<id>/...) for
	// plugins originally Claude-installed, and (c) blur ownership: agentsync
	// owns the user's *view*, not the consumer agent's plugin-manager state.
	// The IngestPlugins read side still discovers plugins from these keys so
	// import can capture them — the asymmetry is intentional.

	return ops, skips, nil
}

// renderMCP converts canonical MCPServers to a FileOp targeting ~/.claude.json
// (user scope) or <project>/.mcp.json (project scope — the upstream Claude Code
// project-MCP location, NOT settings.json). The FileOp carries MergeStrategy
// "merge-json-keys" so Apply preserves foreign keys in a hand-authored .mcp.json.
func (a *Adapter) renderMCP(c source.Canonical, p Paths, scope adapter.Scope) ([]adapter.FileOp, error) {
	targeted := map[string]any{}
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted("claude", m.Server.Agents) {
			continue
		}
		spec := map[string]any{}
		if m.Server.Type != "" {
			spec["type"] = m.Server.Type
		}
		if m.Server.Command != "" {
			spec["command"] = m.Server.Command
		}
		if len(m.Server.Args) > 0 {
			spec["args"] = m.Server.Args
		}
		if len(m.Server.Env) > 0 {
			spec["env"] = m.Server.Env
		}
		if m.Server.URL != "" {
			spec["url"] = m.Server.URL
		}
		if len(m.Server.Headers) > 0 {
			spec["headers"] = m.Server.Headers
		}
		MergeExtra(spec, m.Server.Extra)
		targeted[m.ID] = spec
	}
	if len(targeted) == 0 {
		return nil, nil
	}

	// dest is always non-empty here: Render rejects ScopeProject with an empty
	// project root up front (adapter.RequireProjectRoot), so mcpDest cannot
	// return "" by the time renderMCP runs.
	dest := p.mcpDest(scope)

	ours := map[string]any{"mcpServers": targeted}

	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal mcp: %w", err)
	}

	return []adapter.FileOp{{
		Action:        "write",
		Path:          dest,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "mcp/* (multiple)",
		MergeStrategy: "merge-json-keys",
	}}, nil
}

// agentTargeted reports whether the agents allowlist includes the named agent.
// An empty/nil list or a "*" entry means all agents are targeted.
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
