package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest reads native Claude config files and returns a partial source.Canonical.
// It is the inverse of Render: Ingest(Apply(Render(c))) round-trips to c
// for the components agentsync manages.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP from ~/.claude.json (user) or <proj>/.mcp.json (project — the file
	// `claude mcp add --scope project` writes; settings.json is never project MCP).
	// mcpDest centralizes the scope→file choice shared with renderMCP.
	mcpFile := p.mcpDest(scope)
	if data, err := os.ReadFile(mcpFile); err == nil {
		var top map[string]any
		if err := json.Unmarshal(data, &top); err != nil {
			return c, fmt.Errorf("parse %s: %w", mcpFile, err)
		}
		if servers, ok := top["mcpServers"].(map[string]any); ok {
			for id, raw := range servers {
				spec, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				m := source.MCPServer{ID: id, Server: source.MCPServerSpec{
					Type:    asStr(spec["type"]),
					Command: asStr(spec["command"]),
					Args:    asStrSlice(spec["args"]),
					Env:     asStrMap(spec["env"]),
					URL:     asStr(spec["url"]),
					Headers: asStrMap(spec["headers"]),
					Extra:   ExtraNativeKeys(spec, "type", "command", "args", "env", "url", "headers"),
				}}
				c.MCPServers = append(c.MCPServers, m)
			}
		}
	}

	warn := a.stderr()

	// Skills from ~/.claude/skills/<name>/ (SKILL.md + bundled files)
	if entries, err := os.ReadDir(p.SkillsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillDir := filepath.Join(p.SkillsDir, e.Name())
			data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
			if err != nil {
				continue
			}
			fm, body, lenient, err := ParseFrontmatterWithReport(data)
			if err != nil {
				// A parse error used to silently drop the skill. Warn so the
				// user knows their skill is being skipped and can fix the file.
				fmt.Fprintf(warn, "warning: skipping skill %q: %v\n", e.Name(), err)
				continue
			}
			if lenient {
				fmt.Fprintf(warn, "warning: skill %q frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')\n", e.Name())
			}
			files, err := source.ReadSkillFiles(afero.NewOsFs(), skillDir)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping skill %q: read bundled files: %v\n", e.Name(), err)
				continue
			}
			c.Skills = append(c.Skills, source.Skill{Name: e.Name(), Frontmatter: fm, Body: body, Files: files})
		}
	}

	// Subagents from ~/.claude/agents/<name>.md
	if entries, err := os.ReadDir(p.AgentsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.AgentsDir, e.Name()))
			if err != nil {
				continue
			}
			name := e.Name()[:len(e.Name())-len(".md")]
			fm, body, lenient, err := ParseFrontmatterWithReport(data)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping subagent %q: %v\n", name, err)
				continue
			}
			if lenient {
				fmt.Fprintf(warn, "warning: subagent %q frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')\n", name)
			}
			c.Subagents = append(c.Subagents, source.Subagent{Name: name, Frontmatter: fm, Body: body})
		}
	}

	// Commands from ~/.claude/commands/<name>.md
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
			fm, body, lenient, err := ParseFrontmatterWithReport(data)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping command %q: %v\n", name, err)
				continue
			}
			if lenient {
				fmt.Fprintf(warn, "warning: command %q frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')\n", name)
			}
			c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: fm, Body: body})
		}
	}

	// Hooks from settings.json /hooks/<event>
	if data, err := os.ReadFile(p.Settings); err == nil {
		var top map[string]any
		if json.Unmarshal(data, &top) == nil {
			if hooks, ok := top["hooks"].(map[string]any); ok {
				for event, rawEntries := range hooks {
					entries, ok := rawEntries.([]any)
					if !ok {
						continue
					}
					for _, rawEntry := range entries {
						entry, ok := rawEntry.(map[string]any)
						if !ok {
							continue
						}
						matcher := asStr(entry["matcher"])
						hooksArr, _ := entry["hooks"].([]any)
						for _, rawH := range hooksArr {
							h, ok := rawH.(map[string]any)
							if !ok {
								continue
							}
							c.Hooks = append(c.Hooks, source.Hook{
								Event:   event,
								Matcher: matcher,
								Type:    asStr(h["type"]),
								Command: asStr(h["command"]),
							})
						}
					}
				}
			}
		}
	}

	// LSP servers from settings.json /lspServers/<id>
	if data, err := os.ReadFile(p.Settings); err == nil {
		var top map[string]any
		if json.Unmarshal(data, &top) == nil {
			if lspServers, ok := top["lspServers"].(map[string]any); ok {
				for id, raw := range lspServers {
					spec, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					c.LSPServers = append(c.LSPServers, source.LSPServer{
						ID: id,
						Spec: source.LSPServerSpec{
							Command: asStr(spec["command"]),
							Args:    asStrSlice(spec["args"]),
							Env:     asStrMap(spec["env"]),
							URL:     asStr(spec["url"]),
							Headers: asStrMap(spec["headers"]),
							Extra:   ExtraNativeKeys(spec, "command", "args", "env", "url", "headers"),
						},
					})
				}
			}
		}
	}

	// Memory from CLAUDE.md (verbatim, including fragment markers). The reverse-
	// collapse into AGENTS.md + fragment files happens in the write-back layer
	// (source.CollapseMemoryMarkers, used by import/reconcile), not here.
	if data, err := os.ReadFile(p.Memory); err == nil {
		c.Memory.Body = string(data)
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
