package codex

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest reads Codex's native config files and returns a partial
// source.Canonical. It is the inverse of Render.
//
// Round-trip note: subagents lose the Claude-side `tools`/`color` frontmatter
// (Codex agents have no equivalent), so Ingest reconstructs only the
// `description` + `model` that were written to the agent TOML, plus the body
// from `developer_instructions`. Project-scope slash commands are never written
// (global-only), so they don't ingest at project scope either.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP from config.toml [mcp_servers.<id>]
	if data, err := os.ReadFile(p.Config); err == nil {
		var top map[string]any
		if toml.Unmarshal(data, &top) == nil {
			if servers, ok := top["mcp_servers"].(map[string]any); ok {
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

	// Skills from ~/.agents/skills/<name>/SKILL.md
	if entries, err := os.ReadDir(p.SkillsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.SkillsDir, e.Name(), "SKILL.md"))
			if err != nil {
				continue
			}
			fm, body, err := claude.ParseFrontmatter(data)
			if err != nil {
				continue
			}
			c.Skills = append(c.Skills, source.Skill{Name: e.Name(), Frontmatter: fm, Body: body})
		}
	}

	// Subagents from ~/.codex/agents/<name>.toml (TOML → frontmatter + body)
	if entries, err := os.ReadDir(p.AgentsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.AgentsDir, e.Name()))
			if err != nil {
				continue
			}
			var af codexAgentFile
			if toml.Unmarshal(data, &af) != nil {
				continue
			}
			fm := map[string]any{}
			if af.Description != "" {
				fm["description"] = af.Description
			}
			if af.Model != "" {
				fm["model"] = af.Model
			}
			name := e.Name()[:len(e.Name())-len(".toml")]
			c.Subagents = append(c.Subagents, source.Subagent{Name: name, Frontmatter: fm, Body: af.DeveloperInstructions})
		}
	}

	// Commands from ~/.codex/prompts/<name>.md
	if entries, err := os.ReadDir(p.PromptsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.PromptsDir, e.Name()))
			if err != nil {
				continue
			}
			fm, body, err := claude.ParseFrontmatter(data)
			if err != nil {
				continue
			}
			name := e.Name()[:len(e.Name())-len(".md")]
			c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: fm, Body: body})
		}
	}

	// Hooks from ~/.codex/hooks.json /hooks/<event>
	if data, err := os.ReadFile(p.Hooks); err == nil {
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

	// Memory from AGENTS.md (verbatim)
	if data, err := os.ReadFile(p.Memory); err == nil {
		c.Memory.Body = string(data)
	}

	return c, nil
}
