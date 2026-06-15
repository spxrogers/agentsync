package windsurf

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMCP projects canonical MCP servers into the global
// ~/.codeium/windsurf/mcp_config.json under `mcpServers`. stdio keeps
// command/args/env; a remote server uses Windsurf's `serverUrl` + `headers`.
// op.Content is JSON and the merge-json-keys strategy preserves a hand-authored
// mcp_config.json's foreign servers. p.MCP is empty at project scope (Windsurf
// has no project MCP file); the caller reports a skip for that case.
func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if p.MCP == "" {
		// Project scope: no Windsurf MCP target — report each server as skipped.
		var skips []adapter.Skip
		for _, m := range c.MCPServers {
			if m.Server.Enabled != nil && !*m.Server.Enabled {
				continue
			}
			if !agentTargeted("windsurf", m.Server.Agents) {
				continue
			}
			skips = append(skips, adapter.Skip{
				Component: "mcp", Name: m.ID,
				Reason: "Windsurf MCP config is global-only (~/.codeium/windsurf/mcp_config.json); no project-scope target",
			})
		}
		return nil, skips, nil
	}
	servers := map[string]any{}
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted("windsurf", m.Server.Agents) {
			continue
		}
		servers[m.ID] = windsurfMCPSpec(m.Server)
	}
	if len(servers) == 0 {
		return nil, nil, nil
	}
	ours := map[string]any{"mcpServers": servers}
	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal windsurf mcp: %w", err)
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

// windsurfMCPSpec projects a canonical server into Windsurf's mcp_config.json
// shape. A remote server (http/sse, or untyped-with-url) uses `serverUrl` +
// `headers`; stdio uses command/args/env. Passthrough native keys re-merge via Extra.
func windsurfMCPSpec(s source.MCPServerSpec) map[string]any {
	spec := map[string]any{}
	if isRemote(s) {
		if s.URL != "" {
			spec["serverUrl"] = s.URL
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

// isRemote reports whether a canonical server maps to Windsurf's serverUrl
// (remote) transport. An untyped server carrying a URL but no command is remote.
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

// IngestMCPSpec translates one Windsurf-native server entry (the value under
// mcp_config.json `mcpServers.<id>`) into the canonical MCPServerSpec. A server
// carrying `serverUrl` (or `url`) is canonicalised to the http transport (Windsurf
// does not distinguish sse/http in the config), otherwise stdio. Native keys
// agentsync doesn't model are preserved in Extra.
func IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	url := asStr(raw["serverUrl"])
	if url == "" {
		url = asStr(raw["url"])
	}
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
		Extra:   claude.ExtraNativeKeys(raw, "command", "args", "env", "serverUrl", "url", "headers"),
	}
}

// agentTargeted reports whether the agents allowlist includes windsurf. An
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
