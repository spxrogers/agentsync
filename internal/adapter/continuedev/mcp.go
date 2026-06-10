package continuedev

import (
	"fmt"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMCP projects each canonical MCP server into its own YAML block file at
// `.continue/mcpServers/<id>.yaml` (Continue's documented per-server block: a
// `name`/`version`/`schema` header wrapping a single-element `mcpServers` list).
// stdio keeps command/args/env; a remote server uses Continue's transport name
// (`streamable-http` for HTTP, `sse` for SSE) + `url`, with auth headers under
// `requestOptions.headers`. Each block file is wholly owned by agentsync (a
// whole-file replace), so there is no key-merge — a user's other server blocks in
// the same directory are untouched.
func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	var ops []adapter.FileOp
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted("continue", m.Server.Agents) {
			continue
		}
		block := map[string]any{
			"name":       m.ID,
			"version":    "0.0.1",
			"schema":     "v1",
			"mcpServers": []any{continueMCPServerMap(m.ID, m.Server)},
		}
		body, err := yaml.Marshal(block)
		if err != nil {
			return nil, fmt.Errorf("marshal continue mcp %s: %w", m.ID, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.MCPDir, m.ID+".yaml"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("mcp", m.ID+".toml"),
			MergeStrategy: "replace",
		})
	}
	return ops, nil
}

// continueMCPServerMap builds the inner mcpServers entry for one server.
// requestOptions is rebuilt by deep-merging the canonical Headers into any
// non-headers requestOptions subkeys preserved in Extra (timeout, verifySsl,
// proxy, …) — see IngestMCPSpec — so a captured native block's request options
// survive the round trip instead of being shadowed by the headers map.
// MergeExtra skips keys already present, so the explicit requestOptions here
// wins over the Extra copy.
func continueMCPServerMap(id string, s source.MCPServerSpec) map[string]any {
	srv := map[string]any{"name": id}
	if isRemote(s) {
		srv["type"] = continueTransport(s)
		if s.URL != "" {
			srv["url"] = s.URL
		}
		ro := map[string]any{}
		if rest, ok := s.Extra["requestOptions"].(map[string]any); ok {
			for k, v := range rest {
				ro[k] = v
			}
		}
		if len(s.Headers) > 0 {
			ro["headers"] = s.Headers
		}
		if len(ro) > 0 {
			srv["requestOptions"] = ro
		}
	} else {
		srv["type"] = "stdio"
		if s.Command != "" {
			srv["command"] = s.Command
		}
		if len(s.Args) > 0 {
			srv["args"] = s.Args
		}
		if len(s.Env) > 0 {
			srv["env"] = s.Env
		}
	}
	claude.MergeExtra(srv, s.Extra)
	return srv
}

// isRemote reports whether a canonical server maps to one of Continue's remote
// transports. An untyped server carrying a URL but no command is treated as remote.
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

// continueTransport names Continue's transport for a remote server: SSE keeps
// `sse`; everything else (http, or untyped-with-url) uses streamable HTTP.
func continueTransport(s source.MCPServerSpec) string {
	if s.Type == "sse" {
		return "sse"
	}
	return "streamable-http"
}

// IngestMCPSpec translates one Continue-native server entry (a map under a block's
// `mcpServers` list) into the canonical MCPServerSpec. Inverse of
// continueMCPServerMap: `streamable-http` → http, `sse` → sse, otherwise stdio;
// `requestOptions.headers` → canonical headers, and any OTHER requestOptions
// subkey (timeout, verifySsl, proxy, …) is preserved verbatim in
// Extra["requestOptions"] so the round trip can rebuild the full object —
// dropping it silently would let the next apply destroy it on disk. Native keys
// agentsync doesn't model (e.g. cwd) are preserved in Extra.
func IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	canonType := "stdio"
	switch asStr(raw["type"]) {
	case "streamable-http", "http":
		canonType = "http"
	case "sse":
		canonType = "sse"
	}
	var headers map[string]string
	extra := claude.ExtraNativeKeys(raw, "name", "type", "command", "args", "env", "url", "requestOptions")
	if ro, ok := raw["requestOptions"].(map[string]any); ok {
		headers = asStrMap(ro["headers"])
		residual := map[string]any{}
		for k, v := range ro {
			if k != "headers" {
				residual[k] = v
			}
		}
		if len(residual) > 0 {
			if extra == nil {
				extra = map[string]any{}
			}
			extra["requestOptions"] = residual
		}
	}
	return source.MCPServerSpec{
		Type:    canonType,
		Command: asStr(raw["command"]),
		Args:    asStrSlice(raw["args"]),
		Env:     asStrMap(raw["env"]),
		URL:     asStr(raw["url"]),
		Headers: headers,
		Extra:   extra,
	}
}

// agentTargeted reports whether the agents allowlist includes continue. An
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
