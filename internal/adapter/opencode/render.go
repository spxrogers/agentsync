package opencode

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// Render converts canonical source into FileOps for OpenCode.
func (a *Adapter) Render(c source.Canonical, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
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
	if cmdOps, cmdSkips, err := a.renderCommands(c, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, cmdOps...)
		skips = append(skips, cmdSkips...)
	}
	// Hooks: skip with explanation.
	for _, h := range c.Hooks {
		skips = append(skips, adapter.Skip{
			Component: "hook", Name: h.Event,
			Reason: "OpenCode hooks are JS/TS plugins; shim generation deferred to post-v1",
		})
	}
	// LSP: skip with explanation.
	for _, l := range c.LSPServers {
		skips = append(skips, adapter.Skip{
			Component: "lsp", Name: l.ID,
			Reason: "OpenCode LSP projection deferred to v1.x",
		})
	}
	return ops, skips, nil
}

func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	mcp := map[string]any{}
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted("opencode", m.Server.Agents) {
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
		mcp[m.ID] = spec
	}
	if len(mcp) == 0 {
		return nil, nil
	}
	ours := map[string]any{"mcp": mcp}
	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal opencode mcp: %w", err)
	}
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.Settings,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "mcp/* (multiple)",
		MergeStrategy: "merge-jsonc-keys",
	}}, nil
}

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
