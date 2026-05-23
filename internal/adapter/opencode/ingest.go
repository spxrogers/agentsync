package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/tailscale/hujson"
)

// Ingest reads OpenCode's native config files and returns a partial
// source.Canonical. It is the inverse of Render.
//
// Round-trip note: subagents lose the Claude-side `tools` and `color` fields
// (they were dropped on render because OpenCode has no equivalent). Ingest can
// only reconstruct what is present on disk — `description`, `model`, and the
// `mode` key (which is dropped during ingest because it is an OpenCode-specific
// artifact, not part of canonical).
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP from opencode.json /mcp/<id>
	if data, err := os.ReadFile(p.Settings); err == nil {
		parsed, parseErr := hujson.Parse(data)
		if parseErr == nil {
			parsed.Standardize()
			var top map[string]any
			if json.Unmarshal(parsed.Pack(), &top) == nil {
				if servers, ok := top["mcp"].(map[string]any); ok {
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
						}}
						c.MCPServers = append(c.MCPServers, m)
					}
				}
			}
		}
	}

	// Skills from ~/.claude/skills/<name>/SKILL.md (shared with Claude)
	if entries, err := os.ReadDir(p.ClaudeSkillsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.ClaudeSkillsDir, e.Name(), "SKILL.md"))
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

	// Subagents from <AgentsDir>/<name>.md — frontmatter munged back to canonical:
	// drop `mode` (OpenCode-specific artifact), retain `description` + `model`.
	if entries, err := os.ReadDir(p.AgentsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.AgentsDir, e.Name()))
			if err != nil {
				continue
			}
			fm, body, err := claude.ParseFrontmatter(data)
			if err != nil {
				continue
			}
			// Drop OpenCode-specific `mode` key; it is not canonical.
			delete(fm, "mode")
			name := e.Name()[:len(e.Name())-len(".md")]
			c.Subagents = append(c.Subagents, source.Subagent{Name: name, Frontmatter: fm, Body: body})
		}
	}

	// Commands from <CommandsDir>/<name>.md
	if entries, err := os.ReadDir(p.CommandsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.CommandsDir, e.Name()))
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

	// Memory from AGENTS.md (verbatim)
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
