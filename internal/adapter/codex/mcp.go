package codex

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMCP projects canonical MCP servers into a single op targeting
// ~/.codex/config.toml under the `[mcp_servers]` table. op.Content is JSON
// (`{"mcp_servers": {...}}`) — the pipeline's pointer-merge machinery is
// format-agnostic; the merge-toml-keys strategy makes Apply read/write the
// on-disk config.toml as TOML (see settings.go).
func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	servers := map[string]any{}
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted("codex", m.Server.Agents) {
			continue
		}
		servers[m.ID] = codexMCPSpec(m.Server)
	}
	if len(servers) == 0 {
		return nil, nil
	}
	ours := map[string]any{"mcp_servers": servers}
	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal codex mcp: %w", err)
	}
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.Config,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "mcp/* (multiple)",
		MergeStrategy: "merge-toml-keys",
	}}, nil
}

// codexMCPSpec projects a canonical MCP server into Codex's native config.toml
// shape. A stdio server keeps command/args split (Codex, unlike OpenCode, uses
// a string `command` + `args` array) and env under `env`. A streamable-HTTP
// server carries `url` and `http_headers` (canonical `headers`). Codex has no
// separate SSE transport, so a canonical `sse` server is represented as a URL
// server too. IngestMCPSpec inverts this.
func codexMCPSpec(s source.MCPServerSpec) map[string]any {
	spec := map[string]any{}
	if isRemoteMCP(s) {
		if s.URL != "" {
			spec["url"] = s.URL
		}
		if len(s.Headers) > 0 {
			spec["http_headers"] = s.Headers
		}
		return spec
	}
	if s.Command != "" {
		spec["command"] = s.Command
	}
	if len(s.Args) > 0 {
		spec["args"] = s.Args
	}
	if len(s.Env) > 0 {
		spec["env"] = s.Env
	}
	return spec
}

// isRemoteMCP decides whether a canonical server maps to Codex's URL (HTTP)
// transport. Codex collapses http and sse into a single streamable-HTTP server;
// an untyped server is classified by whether it carries a URL but no command.
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

// IngestMCPSpec translates one Codex-native MCP server table — the value under
// config.toml `[mcp_servers.<id>]` — into the canonical MCPServerSpec. It is the
// inverse of codexMCPSpec (Render). A server carrying a URL is canonicalised to
// the "http" transport; otherwise "stdio". Codex's `http_headers` map back onto
// canonical `headers`.
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
		Headers: asStrMap(raw["http_headers"]),
	}
}

func asStr(v any) string { s, _ := v.(string); return s }

func asStrSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asStrMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}
