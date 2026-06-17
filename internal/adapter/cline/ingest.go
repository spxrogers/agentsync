package cline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest reads Cline's native config and returns a partial source.Canonical. It
// is the inverse of Render, honoring the same scope asymmetry: MCP from the CLI's
// ~/.cline/mcp.json (user scope), rules + workflows from the .clinerules/ tree.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP from ~/.cline/mcp.json (user scope only).
	if p.MCP != "" {
		if data, err := os.ReadFile(p.MCP); err == nil {
			var top map[string]any
			if err := json.Unmarshal(data, &top); err != nil {
				return c, fmt.Errorf("parse %s: %w", p.MCP, err)
			}
			if servers, ok := top["mcpServers"].(map[string]any); ok {
				for id, raw := range servers {
					spec, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					c.MCPServers = append(c.MCPServers, source.MCPServer{ID: id, Server: IngestMCPSpec(spec)})
				}
			}
		}
	}

	// Commands from .clinerules/workflows/<name>.md (project scope; plain markdown).
	if p.WorkflowsDir != "" {
		if entries, err := os.ReadDir(p.WorkflowsDir); err == nil {
			for _, e := range entries {
				if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
					continue
				}
				data, err := os.ReadFile(filepath.Join(p.WorkflowsDir, e.Name()))
				if err != nil {
					continue
				}
				name := e.Name()[:len(e.Name())-len(".md")]
				c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: map[string]any{}, Body: string(data)})
			}
		}
	}

	// Memory from .clinerules/agentsync.md (project scope; plain markdown).
	if p.RulesDir != "" {
		if data, err := os.ReadFile(filepath.Join(p.RulesDir, memoryRuleFile)); err == nil {
			c.Memory.Body = source.StripManagedBanner(string(data)) // banner stripped — see claude/ingest.go
		}
	}

	return c, nil
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
