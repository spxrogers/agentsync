package cline

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMCP projects canonical MCP servers into the Cline CLI's
// `~/.cline/mcp.json` `mcpServers` object (the CLI's documented MCP config; see
// docs.cline.bot → CLI MCP. Cline also documents a unified
// `~/.cline/data/settings/cline_mcp_settings.json` store shared with the IDE
// extension — agentsync deliberately targets the simpler mcp.json surface; do
// not "fix" this to the settings store without re-verifying upstream). Cline infers transport from the keys
// present (no `type` field): stdio keeps command/args/env; a remote server uses
// `url` + `headers`. op.Content is JSON and the merge-json-keys strategy preserves
// a hand-authored mcp.json's foreign servers. p.MCP is empty at project scope
// (Cline has no project MCP file); the caller reports a skip there.
func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if p.MCP == "" {
		var skips []adapter.Skip
		for _, m := range c.MCPServers {
			if m.Server.Enabled != nil && !*m.Server.Enabled {
				continue
			}
			if !agentTargeted("cline", m.Server.Agents) {
				continue
			}
			skips = append(skips, adapter.Skip{
				Component: "mcp", Name: m.ID,
				Reason: "Cline has no project-level MCP file; agentsync targets the Cline CLI's ~/.cline/mcp.json at user scope",
			})
		}
		return nil, skips, nil
	}
	servers := map[string]any{}
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted("cline", m.Server.Agents) {
			continue
		}
		servers[m.ID] = clineMCPSpec(m.Server)
	}
	if len(servers) == 0 {
		return nil, nil, nil
	}
	ours := map[string]any{"mcpServers": servers}
	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal cline mcp: %w", err)
	}
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.MCP,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "mcp/* (multiple)",
		MergeStrategy: "merge-json-keys",
	}}, nil, nil
}

// clineMCPSpec projects a canonical server into Cline's mcp.json shape. Cline has
// no `type` key (transport is inferred): a remote server uses url + headers; stdio
// uses command/args/env. Passthrough native keys (disabled, autoApprove, …)
// re-merge via Extra.
func clineMCPSpec(s source.MCPServerSpec) map[string]any {
	spec := map[string]any{}
	if isRemote(s) {
		if s.URL != "" {
			spec["url"] = s.URL
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

// isRemote reports whether a canonical server maps to Cline's url (remote)
// transport. An untyped server carrying a URL but no command is treated as remote.
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

// IngestMCPSpec translates one Cline-native server entry (the value under
// ~/.cline/mcp.json `mcpServers.<id>`) into the canonical MCPServerSpec. A server
// carrying a `url` is canonicalised to the http transport (Cline does not record a
// sse/http distinction), otherwise stdio. Native keys agentsync doesn't model
// (disabled, autoApprove, …) are preserved in Extra.
func IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	url := asStr(raw["url"])
	typ := "stdio"
	if url != "" {
		typ = "http"
	}
	return source.MCPServerSpec{
		Type:    typ,
		Command: asStr(raw["command"]),
		Args:    asStrSlice(raw["args"]),
		Env:     asStrMap(raw["env"]),
		URL:     url,
		Headers: asStrMap(raw["headers"]),
		Extra:   claude.ExtraNativeKeys(raw, "command", "args", "env", "url", "headers"),
	}
}

// agentTargeted reports whether the agents allowlist includes cline. An empty/nil
// list or a "*" entry means all agents are targeted.
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
