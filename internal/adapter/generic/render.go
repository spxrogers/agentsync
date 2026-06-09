package generic

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// Render projects the canonical model into this breadth-tier agent's memory
// (rules) file and, when the spec declares one, its mcpServers JSON. Every other
// component (skills, subagents, commands, hooks, LSP) is reported as a skip: the
// generic tier deliberately covers memory + MCP only. A component the spec does
// not support at the requested scope (e.g. user-scope memory for a project-only
// agent) is likewise reported as a skip rather than written somewhere wrong.
func (a *Adapter) Render(r secrets.Resolved, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, nil, err
	}
	c := r.Canonical() //nolint:forbidigo // sanctioned render egress: project the resolved model into FileOps (never written back to source)
	renderC := c
	if scope == adapter.ScopeProject && c.Project != nil {
		renderC = *c.Project
	}

	var ops []adapter.FileOp
	var skips []adapter.Skip

	// Memory → the agent's rules file (plain markdown body).
	if renderC.Memory.Body != "" {
		if memPath := a.memoryPath(scope, project); memPath != "" {
			body := source.ExpandMemoryImports(renderC.Memory.Body, renderC.Memory.Fragments)
			ops = append(ops, adapter.FileOp{
				Action:        "write",
				Path:          memPath,
				Content:       []byte(body),
				Mode:          0o644,
				SourceID:      "memory/AGENTS.md",
				MergeStrategy: "replace",
			})
		} else if a.spec.Memory.User != "" || a.spec.Memory.Project != "" {
			// Supported on the other scope only — report the scope gap.
			skips = append(skips, adapter.Skip{
				Component: "memory", Name: a.spec.Name,
				Reason: fmt.Sprintf("%s memory has no %s-scope target", a.spec.Name, scope.String()),
			})
		}
	}

	// MCP → the agent's mcpServers JSON.
	if mcpOps, mcpSkips, err := a.renderMCP(renderC, scope, project); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, mcpOps...)
		skips = append(skips, mcpSkips...)
	}

	// Components the breadth tier does not project — reported, never silent.
	skips = append(skips, a.unsupportedSkips(renderC)...)
	return ops, skips, nil
}

// renderMCP builds the mcpServers JSON for the spec's shape, or reports skips when
// the spec has no MCP target at this scope.
func (a *Adapter) renderMCP(c source.Canonical, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	mcpPath := a.mcpPath(scope, project)
	servers := map[string]any{}
	var skips []adapter.Skip
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted(a.spec.Name, m.Server.Agents) {
			continue
		}
		if mcpPath == "" {
			skips = append(skips, adapter.Skip{
				Component: "mcp", Name: m.ID,
				Reason: a.mcpSkipReason(scope),
			})
			continue
		}
		servers[m.ID] = mcpServerMap(a.spec.MCP, m.Server)
	}
	if len(servers) == 0 {
		return nil, skips, nil
	}
	ours := map[string]any{a.spec.MCP.rootKey(): servers}
	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal %s mcp: %w", a.spec.Name, err)
	}
	return []adapter.FileOp{{
		Action:        "write",
		Path:          mcpPath,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "mcp/* (multiple)",
		MergeStrategy: "merge-json-keys",
	}}, skips, nil
}

// mcpSkipReason explains why MCP has no target at this scope.
func (a *Adapter) mcpSkipReason(scope adapter.Scope) string {
	if !a.spec.MCP.supported() {
		return fmt.Sprintf("%s has no file-based MCP config agentsync targets", a.spec.Name)
	}
	return fmt.Sprintf("%s MCP has no %s-scope target", a.spec.Name, scope.String())
}

// unsupportedSkips reports the components present in the canonical model that the
// breadth tier never projects, so the coverage report is honest.
func (a *Adapter) unsupportedSkips(c source.Canonical) []adapter.Skip {
	var skips []adapter.Skip
	reason := func(comp string) string {
		return fmt.Sprintf("agentsync's breadth-tier adapter for %s projects memory + MCP only (no %s)", a.spec.Name, comp)
	}
	for _, s := range c.Skills {
		skips = append(skips, adapter.Skip{Component: "skill", Name: s.Name, Reason: reason("skills")})
	}
	for _, s := range c.Subagents {
		skips = append(skips, adapter.Skip{Component: "subagent", Name: s.Name, Reason: reason("subagents")})
	}
	for _, cmd := range c.Commands {
		skips = append(skips, adapter.Skip{Component: "command", Name: cmd.Name, Reason: reason("commands")})
	}
	for _, h := range c.Hooks {
		skips = append(skips, adapter.Skip{Component: "hook", Name: h.Event, Reason: reason("hooks")})
	}
	for _, l := range c.LSPServers {
		skips = append(skips, adapter.Skip{Component: "lsp", Name: l.ID, Reason: reason("LSP")})
	}
	return skips
}

// mcpServerMap projects a canonical MCP server into the on-disk shape described
// by the spec's MCPTarget dialect (root key handled by the caller; this builds
// one server entry).
func mcpServerMap(t MCPTarget, s source.MCPServerSpec) map[string]any {
	spec := map[string]any{}
	remote := isRemote(s)
	if t.TransportKey != "" {
		if remote {
			spec[t.TransportKey] = t.remoteValue()
		} else {
			spec[t.TransportKey] = t.stdioValue()
		}
	}
	if remote {
		if s.URL != "" {
			spec[t.remoteURLKey()] = s.URL
		}
		if len(s.Headers) > 0 {
			spec["headers"] = s.Headers
		}
	} else {
		if s.Command != "" {
			spec["command"] = s.Command
		}
		if len(s.Args) > 0 {
			spec["args"] = s.Args
		}
		if len(s.Env) > 0 {
			spec["env"] = s.Env
		}
	}
	claude.MergeExtra(spec, s.Extra)
	return spec
}

// isRemote reports whether a server maps to a remote (url) transport. An untyped
// server with a URL but no command is treated as remote.
func isRemote(s source.MCPServerSpec) bool {
	switch s.Type {
	case "http", "sse":
		return true
	case "stdio":
		return false
	default:
		return s.URL != "" && s.Command == ""
	}
}
