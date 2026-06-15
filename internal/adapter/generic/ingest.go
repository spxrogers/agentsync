package generic

import (
	"fmt"
	"os"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest reads this breadth-tier agent's memory + MCP back into a partial
// canonical. Inverse of Render (memory + MCP only; the components the tier never
// projects are simply not read).
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	var c source.Canonical

	if mcpPath := a.mcpPath(scope, project); mcpPath != "" {
		if data, err := os.ReadFile(mcpPath); err == nil {
			// JSONC-tolerant (shared jsonkeys.DecodeJSONC): comments/trailing
			// commas in a hand-edited settings file (Zed, Copilot, Amp) ingest
			// cleanly, and json.Number survives for large foreign integers.
			top, err := jsonkeys.DecodeJSONC(data)
			if err != nil {
				return c, fmt.Errorf("parse %s: %w", mcpPath, err)
			}
			if servers, ok := top[a.spec.MCP.rootKey()].(map[string]any); ok {
				for id, raw := range servers {
					spec, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					c.MCPServers = append(c.MCPServers, source.MCPServer{ID: id, Server: ingestMCPSpec(a.spec.MCP, spec)})
				}
			}
		}
	}

	if memPath := a.memoryPath(scope, project); memPath != "" {
		if data, err := os.ReadFile(memPath); err == nil {
			c.Memory.Body = string(data)
		}
	}

	return c, nil
}

// ingestMCPSpec is the inverse of mcpServerMap for the spec's dialect. When the
// dialect names a transport field it is trusted (the stdio value — or the
// universal "stdio" alias several agents document alongside their own value —
// maps to stdio, "sse" to sse, everything else to its http meaning); otherwise
// transport is inferred from which URL field is present. For a dual-URL dialect
// (SSEURLKey set), RemoteURLKey wins when both fields are present (the upstream
// precedence) and a server read from SSEURLKey canonicalizes as `sse`.
//
// Acknowledged subset: asStr/asStrSlice/asStrMap capture only string-shaped
// values — a non-string form of a MODELED key (e.g. Zed's legacy nested
// `command` object) is not captured and, being a modeled key, is excluded from
// Extra too; re-verify against upstream before teaching a dialect a second
// shape.
func ingestMCPSpec(t MCPTarget, raw map[string]any) source.MCPServerSpec {
	url := asStr(raw[t.remoteURLKey()])
	sseURL := ""
	if t.SSEURLKey != "" {
		sseURL = asStr(raw[t.SSEURLKey])
	}
	canonType := "stdio"
	switch {
	case t.TransportKey != "" && asStr(raw[t.TransportKey]) != "":
		tv := asStr(raw[t.TransportKey])
		switch {
		case tv == t.stdioValue() || tv == "stdio":
			canonType = "stdio"
		case tv == "sse":
			canonType = "sse"
		default: // http, streamable-http, remote, the agent's RemoteValue, …
			canonType = "http"
		}
	case url != "":
		canonType = "http"
	case sseURL != "":
		canonType = "sse"
		url = sseURL
	}
	excluded := []string{t.TransportKey, "command", "args", "env", t.remoteURLKey(), "headers"}
	if t.SSEURLKey != "" {
		excluded = append(excluded, t.SSEURLKey)
	}
	return source.MCPServerSpec{
		Type:    canonType,
		Command: asStr(raw["command"]),
		Args:    asStrSlice(raw["args"]),
		Env:     asStrMap(raw["env"]),
		URL:     url,
		Headers: asStrMap(raw["headers"]),
		Extra:   claude.ExtraNativeKeys(raw, excluded...),
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
