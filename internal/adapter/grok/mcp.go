package grok

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

func renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	servers := map[string]any{}
	var skips []adapter.Skip
	for _, m := range c.MCPServers {
		s := m.Server
		if s.Enabled != nil && !*s.Enabled {
			continue
		}
		if len(s.Agents) > 0 && !slices.Contains(s.Agents, "*") && !slices.Contains(s.Agents, "grok") {
			continue
		}
		spec := map[string]any{}
		// Grok infers transport from command/url. Keep every modeled native
		// field, including env on remote servers, rather than silently dropping
		// fields while choosing a transport.
		if s.Command != "" {
			spec["command"] = s.Command
		}
		if len(s.Args) > 0 {
			spec["args"] = s.Args
		}
		if len(s.Env) > 0 {
			spec["env"] = s.Env
		}
		if s.URL != "" {
			spec["url"] = s.URL
		}
		if len(s.Headers) > 0 {
			spec["headers"] = s.Headers
		}
		claude.MergeExtra(spec, s.Extra)
		servers[m.ID] = spec
		if s.Type == "sse" {
			skips = append(skips, adapter.Skip{
				Component: "mcp", Name: m.ID, Kind: adapter.SkipReduced,
				Reason: "Grok uses a URL transport without a separate SSE type; capture normalizes type to http",
			})
		}
		// Native ${VAR} expansion also runs on resolved values. Never print
		// those values or invent an escaping syntax Grok does not document.
		fields := []string{}
		for field, values := range map[string][]string{
			"command": {s.Command}, "url": {s.URL}, "args": s.Args,
		} {
			for _, value := range values {
				if strings.Contains(value, "${") {
					fields = append(fields, field)
					break
				}
			}
		}
		for field, values := range map[string]map[string]string{"env": s.Env, "headers": s.Headers} {
			for _, value := range values {
				if strings.Contains(value, "${") {
					fields = append(fields, field)
					break
				}
			}
		}
		if len(fields) > 0 {
			slices.Sort(fields)
			skips = append(skips, adapter.Skip{
				Component: "mcp", Name: m.ID, Kind: adapter.SkipReduced,
				Reason: "Grok expands native variable references in " + strings.Join(fields, ", ") + "; values are written verbatim",
			})
		}
	}
	if len(servers) == 0 {
		return nil, skips, nil
	}
	body, err := json.MarshalIndent(map[string]any{"mcp_servers": servers}, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal grok mcp: %w", err)
	}
	return []adapter.FileOp{{
		Path: p.Config, Content: append(body, '\n'), Mode: 0o644,
		SourceID: "mcp/* (multiple)", MergeStrategy: "merge-toml-keys",
	}}, skips, nil
}

// IngestMCPSpec translates one Grok-native MCP server table — the value under
// config.toml `[mcp_servers.<id>]` — into the canonical MCPServerSpec. It is the
// inverse of renderMCP. Grok's headers key is "headers" (unlike Codex's
// "http_headers"). A server carrying a URL is canonicalised to the "http"
// transport; otherwise "stdio". Native extra fields survive capture through
// Extra.
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

// IngestMCPSpec satisfies adapter.MCPSpecIngester.
func (a *Adapter) IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	return IngestMCPSpec(raw)
}

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
