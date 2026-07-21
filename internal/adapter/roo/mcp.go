package roo

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMCP projects canonical MCP servers into the project-level `.roo/mcp.json`
// `mcpServers` object. stdio keeps command/args/env; a remote server uses Roo's
// explicit `type` (`streamable-http` for HTTP, `sse` for SSE) + `url` + `headers`.
// op.Content is JSON and the merge-json-keys strategy preserves a hand-authored
// .roo/mcp.json's foreign servers (read → merge by server name → write — the
// exact pattern rulesync and ruler use). p.MCP is empty at user scope (Roo's
// global MCP is VS Code globalStorage); the caller reports a skip there.
func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if p.MCP == "" {
		var skips []adapter.Skip
		for _, m := range c.MCPServers {
			if m.Server.Enabled != nil && !*m.Server.Enabled {
				continue
			}
			if !agentTargeted("roo", m.Server.Agents) {
				continue
			}
			skips = append(skips, adapter.Skip{
				Component: "mcp", Name: m.ID,
				Reason: "Roo global MCP lives in VS Code globalStorage (OS/editor-specific); agentsync targets the project-level .roo/mcp.json (use project scope)",
				Kind:   adapter.SkipDropped,
			})
		}
		return nil, skips, nil
	}
	servers := map[string]any{}
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted("roo", m.Server.Agents) {
			continue
		}
		servers[m.ID] = rooMCPSpec(m.Server)
	}
	if len(servers) == 0 {
		return nil, nil, nil
	}
	ours := map[string]any{"mcpServers": servers}
	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal roo mcp: %w", err)
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

// rooMCPSpec projects a canonical server into Roo's mcp.json shape. A remote
// server carries an explicit `type` (streamable-http/sse) + url + headers; stdio
// uses command/args/env. Passthrough native keys (cwd, timeout, alwaysAllow, …)
// re-merge via Extra.
func rooMCPSpec(s source.MCPServerSpec) map[string]any {
	spec := map[string]any{}
	if isRemote(s) {
		spec["type"] = rooTransport(s)
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

// isRemote reports whether a canonical server maps to a Roo remote transport. An
// UNTYPED server (Type == "") carrying a URL but no command is treated as remote —
// see the transport-normalization note on IngestMCPSpec: such a server round-trips
// to a stable canonical Type == "http".
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

// rooTransport names Roo's transport for a remote server: SSE keeps `sse`;
// everything else (http, or untyped-with-url) uses streamable HTTP.
func rooTransport(s source.MCPServerSpec) string {
	if s.Type == "sse" {
		return "sse"
	}
	return "streamable-http"
}

// IngestMCPSpec translates one Roo-native server entry (the value under
// .roo/mcp.json `mcpServers.<id>`) into the canonical MCPServerSpec. Inverse of
// rooMCPSpec: `streamable-http`/`http` → http, `sse` → sse, and stdio otherwise.
// Native keys agentsync doesn't model (cwd, timeout, alwaysAllow, …) are preserved
// in Extra.
//
// Transport normalization (documented in docs/capability-matrix.md → "Roo Code /
// MCP remote"): Roo records a remote server's transport in an explicit `type`
// (`streamable-http`/`sse`), so a native `sse` server round-trips `sse → sse`
// losslessly. A canonical server with NO explicit transport — Type == "" with a
// URL and no command — is treated as remote by rooMCPSpec.isRemote and rendered as
// `type: streamable-http`, so it canonicalizes to `http` on capture and thereafter
// round-trips `http → streamable-http → http` STABLY. To make that same
// normalization hold for a HAND-authored native entry that carries only a `url`
// (no `type`, no `command`), a url-bearing/command-less untyped entry is also
// canonicalized to `http` here — otherwise it would be misread as `stdio` and its
// URL dropped on the next render. So the single rule, in both directions, is:
// "url + no command, no explicit transport ⇒ remote http".
func IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	command := asStr(raw["command"])
	url := asStr(raw["url"])
	canonType := "stdio"
	switch asStr(raw["type"]) {
	case "streamable-http", "http":
		canonType = "http"
	case "sse":
		canonType = "sse"
	case "stdio":
		canonType = "stdio"
	default:
		// No explicit Roo transport `type`. A url-bearing, command-less entry is a
		// remote server (matching rooMCPSpec.isRemote) → normalize to http.
		if url != "" && command == "" {
			canonType = "http"
		}
	}
	return source.MCPServerSpec{
		Type:    canonType,
		Command: command,
		Args:    asStrSlice(raw["args"]),
		Env:     asStrMap(raw["env"]),
		URL:     url,
		Headers: asStrMap(raw["headers"]),
		Extra:   claude.ExtraNativeKeys(raw, "type", "command", "args", "env", "url", "headers"),
	}
}

// agentTargeted reports whether the agents allowlist includes roo. An empty/nil
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
