package gemini

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMCP projects canonical MCP servers into a single op targeting
// `.gemini/settings.json` under the `mcpServers` object. A stdio server keeps
// command/args/env; a remote server uses Gemini's two-key transport split —
// `url` for SSE, `httpUrl` for HTTP streaming — with `headers`. op.Content is
// JSON and the merge-json-keys strategy preserves a hand-authored settings.json's
// foreign keys (and the `hooks` section the adapter also owns).
func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	servers := map[string]any{}
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted("gemini", m.Server.Agents) {
			continue
		}
		servers[m.ID] = geminiMCPSpec(m.Server)
	}
	if len(servers) == 0 {
		return nil, nil
	}
	ours := map[string]any{"mcpServers": servers}
	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal gemini mcp: %w", err)
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

// geminiMCPSpec projects a canonical MCP server into Gemini's settings.json
// shape. stdio → command/args/env; SSE → url; HTTP → httpUrl; both remote
// transports carry headers. Passthrough native keys (cwd, timeout, trust)
// re-merge via Extra. IngestMCPSpec inverts this.
func geminiMCPSpec(s source.MCPServerSpec) map[string]any {
	spec := map[string]any{}
	switch {
	case isSSE(s):
		if s.URL != "" {
			spec["url"] = s.URL
		}
		if len(s.Headers) > 0 {
			spec["headers"] = s.Headers
		}
	case isHTTP(s):
		if s.URL != "" {
			spec["httpUrl"] = s.URL
		}
		if len(s.Headers) > 0 {
			spec["headers"] = s.Headers
		}
	default:
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

// isSSE reports whether a canonical server maps to Gemini's `url` (SSE) key.
func isSSE(s source.MCPServerSpec) bool { return s.Type == "sse" }

// isHTTP reports whether a canonical server maps to Gemini's `httpUrl` (HTTP
// streaming) key. An untyped server carrying a URL but no command is treated as
// HTTP (the modern default).
func isHTTP(s source.MCPServerSpec) bool {
	if s.Type == "http" {
		return true
	}
	return s.Type == "" && s.URL != "" && s.Command == ""
}

// IngestMCPSpec translates one Gemini-native MCP server table — the value under
// settings.json `mcpServers.<id>` — into the canonical MCPServerSpec. It is the
// inverse of geminiMCPSpec: `httpUrl` → http transport, `url` → sse transport,
// otherwise stdio. Native keys agentsync doesn't model are kept in Extra.
func IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	httpURL := asStr(raw["httpUrl"])
	sseURL := asStr(raw["url"])
	typ := "stdio"
	url := ""
	switch {
	case httpURL != "":
		typ, url = "http", httpURL
	case sseURL != "":
		typ, url = "sse", sseURL
	}
	return source.MCPServerSpec{
		Type:    typ,
		Command: asStr(raw["command"]),
		Args:    asStrSlice(raw["args"]),
		Env:     asStrMap(raw["env"]),
		URL:     url,
		Headers: asStrMap(raw["headers"]),
		Extra:   claude.ExtraNativeKeys(raw, "command", "args", "env", "url", "httpUrl", "headers"),
	}
}

// agentTargeted reports whether the agents allowlist includes gemini. An
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
