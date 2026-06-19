package opencode

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// Render converts the resolved canonical into FileOps for OpenCode.
func (a *Adapter) Render(r secrets.Resolved, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, nil, err
	}
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
	if cmdOps, cmdSkips, err := a.renderCommands(renderC, p); err != nil {
		return nil, nil, err
	} else {
		ops = append(ops, cmdOps...)
		skips = append(skips, cmdSkips...)
	}
	// Hooks: skip with explanation.
	for _, h := range renderC.Hooks {
		skips = append(skips, adapter.Skip{
			Component: "hook", Name: h.Event,
			Reason: "OpenCode hooks are JS/TS plugins; shim generation deferred to post-v1",
			Kind:   adapter.SkipDropped,
		})
	}
	// LSP: skip with explanation.
	for _, l := range renderC.LSPServers {
		skips = append(skips, adapter.Skip{
			Component: "lsp", Name: l.ID,
			Reason: "OpenCode LSP projection deferred to v1.x",
			Kind:   adapter.SkipDropped,
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
		mcp[m.ID] = opencodeMCPSpec(m.Server)
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

// opencodeMCPSpec projects a canonical MCP server into OpenCode's native shape
// (https://opencode.ai/config.json): `type` is "local" or "remote", a local
// server's `command` is a string ARRAY ([command, ...args]), env vars live under
// `environment` (not `env`), and a remote server carries `url`/`headers`. The
// canonical model instead keeps command/args split and uses stdio/http/sse, so
// translate. Ingest (canonicalMCPType / opencodeIngestCommand) inverts this.
func opencodeMCPSpec(s source.MCPServerSpec) map[string]any {
	spec := map[string]any{}
	if isRemoteMCP(s) {
		spec["type"] = "remote"
		if s.URL != "" {
			spec["url"] = s.URL
		}
		if len(s.Headers) > 0 {
			spec["headers"] = s.Headers
		}
		claude.MergeExtra(spec, s.Extra)
		return spec
	}
	spec["type"] = "local"
	if cmd := commandArray(s); len(cmd) > 0 {
		spec["command"] = cmd
	}
	if len(s.Env) > 0 {
		spec["environment"] = s.Env
	}
	claude.MergeExtra(spec, s.Extra)
	return spec
}

// isRemoteMCP decides whether a canonical server maps to OpenCode's "remote"
// type. OpenCode collapses http and sse into a single "remote" transport; an
// untyped server is classified by whether it carries a URL but no command.
func isRemoteMCP(s source.MCPServerSpec) bool {
	switch s.Type {
	case "http", "sse":
		return true
	case "stdio":
		return false
	default:
		return s.URL != "" && s.Command == ""
	}
}

// commandArray flattens a canonical command + args into OpenCode's single
// command string array.
func commandArray(s source.MCPServerSpec) []string {
	var cmd []string
	if s.Command != "" {
		cmd = append(cmd, s.Command)
	}
	return append(cmd, s.Args...)
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
