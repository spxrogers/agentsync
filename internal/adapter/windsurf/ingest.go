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
		data, present, err := adapter.ReadFileOptional(p.MCP)
		if err != nil {
			return c, fmt.Errorf("read %s: %w", p.MCP, err)
		}
		if present {
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
		entries, present, err := adapter.ReadDirOptional(p.WorkflowsDir)
		if err != nil {
			return c, fmt.Errorf("read workflows dir %s: %w", p.WorkflowsDir, err)
		}
		if present {
			for _, e := range entries {
				if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
					continue
				}
				name := e.Name()[:len(e.Name())-len(".md")]
				data, err := os.ReadFile(filepath.Join(p.WorkflowsDir, e.Name()))
				if err != nil {
					if !os.IsNotExist(err) {
						fmt.Fprintf(warn, "warning: skipping workflow %q: read: %v\n", name, err)
					}
					continue
				}
				// Windsurf workflows are plain markdown with no honored frontmatter,
				// but a user may have hand-authored a leading `---`…`---` block; strip
				// it so a stray fence never folds into the canonical command body.
				body, _ := stripLeadingFrontmatter(string(data))
				c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: map[string]any{}, Body: body})
			}
		}
	}

	// Memory: the workspace rule at project scope (activation frontmatter
	// stripped), the global rules file at user scope (frontmatter-less). Both
	// then drop the agentsync managed-file banner — a render-time decoration with
	// no canonical home (see claude/ingest.go).
	if p.RulesDir != "" {
		ruleFile := filepath.Join(p.RulesDir, memoryRuleFile)
		data, present, err := adapter.ReadFileOptional(ruleFile)
		if err != nil {
			return c, fmt.Errorf("read memory rule %s: %w", ruleFile, err)
		}
		if present {
			body, _, foreign := stripMemoryRuleFrontmatter(data)
			// Warn only when a FOREIGN (non-`always_on`) fence was stripped — its
			// activation mode has no canonical home. A frontmatter-less rule (no
			// fence) does not warn, and the agentsync block round-trips silently.
			if foreign {
				fmt.Fprintf(warn, "warning: %s does not start with the agentsync-rendered `trigger: always_on` frontmatter; Windsurf activation metadata has no canonical home and is not captured\n", ruleFile)
			}
			c.Memory.Body = source.StripManagedBanner(body)
		}
	}
	if p.GlobalRules != "" {
		data, present, err := adapter.ReadFileOptional(p.GlobalRules)
		if err != nil {
			return c, fmt.Errorf("read global rules %s: %w", p.GlobalRules, err)
		}
		if present {
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
