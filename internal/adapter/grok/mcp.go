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

// IngestMCPSpec preserves unmodeled native keys through the shared Extra
// contract. Typed decoding refuses malformed modeled fields instead of
// capturing an incomplete server that a later apply would overwrite.
func IngestMCPSpec(raw map[string]any) (source.MCPServerSpec, error) {
	var native struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return source.MCPServerSpec{}, err
	}
	if err := json.Unmarshal(data, &native); err != nil {
		return source.MCPServerSpec{}, err
	}
	typ := "stdio"
	if native.URL != "" {
		typ = "http"
	}
	return source.MCPServerSpec{
		Type: typ, Command: native.Command, Args: native.Args,
		Env: native.Env, URL: native.URL, Headers: native.Headers,
		Extra: claude.ExtraNativeKeys(raw, "command", "args", "env", "url", "headers"),
	}, nil
}
