package windsurf

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest reads Windsurf's native config and returns a partial source.Canonical.
// It is the inverse of Render, honoring the same scope asymmetry: MCP and the
// global rules file from `~/.codeium/windsurf/` plus global workflows at user
// scope; workspace rules + workflows from the project `.windsurf/` tree at
// project scope. The agentsync-rendered `trigger: always_on` frontmatter on the
// workspace rule is stripped so the canonical memory body stays byte-clean.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical
	warn := a.stderr()

	// MCP from ~/.codeium/windsurf/mcp_config.json (user scope only).
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

	// Commands from workflows (.windsurf/workflows/ at project scope,
	// ~/.codeium/windsurf/global_workflows/ at user scope; plain markdown).
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

	// Memory: the workspace rule at project scope (activation frontmatter
	// stripped), the global rules file at user scope (frontmatter-less). Both
	// then drop the agentsync managed-file banner — a render-time decoration with
	// no canonical home (see claude/ingest.go).
	if p.RulesDir != "" {
		if data, err := os.ReadFile(filepath.Join(p.RulesDir, memoryRuleFile)); err == nil {
			body, exact := stripMemoryRuleFrontmatter(data)
			if !exact {
				fmt.Fprintf(warn, "warning: %s does not start with the agentsync-rendered `trigger: always_on` frontmatter; Windsurf activation metadata has no canonical home and is not captured\n", filepath.Join(p.RulesDir, memoryRuleFile))
			}
			c.Memory.Body = source.StripManagedBanner(body)
		}
	}
	if p.GlobalRules != "" {
		if data, err := os.ReadFile(p.GlobalRules); err == nil {
			c.Memory.Body = source.StripManagedBanner(string(data))
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
