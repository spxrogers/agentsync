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
						fmt.Fprintf(a.stderr(), "warning: skipping workflow %q: read: %v\n", name, err)
					}
					continue
				}
				// Windsurf workflows are plain markdown with no honored frontmatter,
				// but a user may have hand-authored a leading `---`…`---` block; strip
				// it so a stray fence never folds into the canonical command body —
				// and warn (mirroring the rules path), since the strip deletes
				// user-authored bytes that have no canonical home.
				body, stripped := stripLeadingFrontmatter(string(data))
				if stripped {
					fmt.Fprintf(a.stderr(), "warning: workflow %q starts with a `---` frontmatter fence; Windsurf workflows honor no frontmatter, so the fence has no canonical home and is not captured\n", name)
				}
				c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: map[string]any{}, Body: body})
			}
		}
	}

	// Memory: exactly one source per scope, and they are mutually exclusive — the
	// workspace rule at project scope (activation frontmatter stripped), the global
	// rules file at user scope (frontmatter-less). readMemoryBody reads the project
	// rule when present and the global file ONLY otherwise, so a populated project
	// body can never be clobbered by a stray global read (see its doc comment); it
	// surfaces a real read error loudly while treating os.IsNotExist as absent (#159).
	body, ok, err := a.readMemoryBody(p)
	if err != nil {
		return c, err
	}
	if ok {
		c.Memory.Body = body
	}

	return c, nil
}

// readMemoryBody projects the scope's single memory source into the canonical
// body, dropping the agentsync managed-file banner (a render-time decoration
// with no canonical home; see claude/ingest.go).
//
// The two sources are mutually exclusive per scope: paths.go sets RulesDir only
// at project scope and GlobalRules only at user scope. The project workspace rule
// is read when present, and the global rules file ONLY when there is no project
// rule — an else-if guard (not two independent reads that both assign
// Memory.Body) so a future paths.go change can never let the global read
// overwrite a body already populated from the project rule. Returns the stripped
// body, whether a source was found, and a non-nil error ONLY for a real
// read/parse failure: an absent file (os.IsNotExist) is a silent skip, but a
// present-but-unreadable file is surfaced loudly rather than read as "cleared"
// (per #159, via adapter.ReadFileOptional).
func (a *Adapter) readMemoryBody(p Paths) (string, bool, error) {
	if p.RulesDir != "" {
		ruleFile := filepath.Join(p.RulesDir, memoryRuleFile)
		data, present, err := adapter.ReadFileOptional(ruleFile)
		if err != nil {
			return "", false, fmt.Errorf("read memory rule %s: %w", ruleFile, err)
		}
		if !present {
			return "", false, nil
		}
		body, _, foreign := stripMemoryRuleFrontmatter(data)
		// Warn only when a FOREIGN (non-`always_on`) fence was stripped — its
		// activation mode has no canonical home. A frontmatter-less rule (no
		// fence) does not warn, and the agentsync block round-trips silently.
		if foreign {
			fmt.Fprintf(a.stderr(), "warning: %s does not start with the agentsync-rendered `trigger: always_on` frontmatter; Windsurf activation metadata has no canonical home and is not captured\n", ruleFile)
		}
		return source.StripManagedBanner(body), true, nil
	}
	if p.GlobalRules != "" {
		data, present, err := adapter.ReadFileOptional(p.GlobalRules)
		if err != nil {
			return "", false, fmt.Errorf("read global rules %s: %w", p.GlobalRules, err)
		}
		if !present {
			return "", false, nil
		}
		return source.StripManagedBanner(string(data)), true, nil
	}
	return "", false, nil
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
