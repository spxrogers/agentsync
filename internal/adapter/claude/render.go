package claude

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// Render produces the full set of FileOps for a given canonical model.
// Pure function: returns the same output for the same input (disk reads are
// treated as fixed inputs for the purposes of the merge-json-keys strategy).
func (a *Adapter) Render(c source.Canonical, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	paths := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)

	var ops []adapter.FileOp
	var skips []adapter.Skip

	// 1. MCP -> .claude.json (user) or settings.json (project)
	if mcpOps, err := a.renderMCP(c, paths, scope); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, mcpOps...)
	}

	// 2. Memory -> CLAUDE.md
	if memOps, err := a.renderMemory(c, paths); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, memOps...)
	}

	// 3. Skills -> ~/.claude/skills/<name>/SKILL.md
	if skillOps, err := a.renderSkills(c, paths); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, skillOps...)
	}

	// Tasks 8-11: subagents, commands, hooks, LSP — implemented later.

	return ops, skips, nil
}

// renderMCP converts canonical MCPServers to a FileOp targeting .claude.json
// (user scope) or settings.json (project scope). The FileOp carries
// MergeStrategy "merge-json-keys" so Apply can preserve foreign keys.
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
		targeted[m.ID] = spec
	}
	if len(targeted) == 0 {
		return nil, nil
	}

	ours := map[string]any{"mcpServers": targeted}

	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal mcp: %w", err)
	}

	var dest string
	if scope == adapter.ScopeProject {
		dest = p.Settings
	} else {
		dest = p.DotClaude
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
