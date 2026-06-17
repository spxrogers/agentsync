package roo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest reads Roo's native config and returns a partial source.Canonical. It is
// the inverse of Render: MCP from the project-level .roo/mcp.json, rules + commands
// from the .roo/ tree at whichever scope was requested.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP from .roo/mcp.json (project scope only).
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

	warn := a.stderr()

	// Commands from .roo/commands/<name>.md (markdown + frontmatter).
	if entries, err := os.ReadDir(p.CommandsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.CommandsDir, e.Name()))
			if err != nil {
				continue
			}
			name := e.Name()[:len(e.Name())-len(".md")]
			fm, body, lenient, err := claude.ParseFrontmatterWithReport(data)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping command %q: %v\n", name, err)
				continue
			}
			if lenient {
				fmt.Fprintf(warn, "warning: command %q frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')\n", name)
			}
			// Keep only the canonical-relevant frontmatter (description,
			// argument-hint); Roo-specific keys (e.g. mode) have no canonical
			// home and are dropped — with a warning, since a captured command
			// re-applies without them.
			cf := map[string]any{}
			var dropped []string
			for k, v := range fm {
				if rooCommandKnownKeys[k] {
					cf[k] = v
				} else {
					dropped = append(dropped, k)
				}
			}
			if len(dropped) > 0 {
				sort.Strings(dropped)
				fmt.Fprintf(warn, "warning: command %q frontmatter keys not modeled by agentsync dropped on import: %s\n", name, strings.Join(dropped, ", "))
			}
			c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: cf, Body: body})
		}
	}

	// Memory from .roo/rules/agentsync.md (banner stripped — see claude/ingest.go).
	if data, err := os.ReadFile(filepath.Join(p.RulesDir, memoryRuleFile)); err == nil {
		c.Memory.Body = source.StripManagedBanner(string(data))
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
