package cursor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest reads Cursor's native config files and returns a partial
// source.Canonical. It is the inverse of Render: Ingest(Apply(Render(c)))
// round-trips to c for the components agentsync manages (modulo the documented
// projected loss — subagent tools/color, command frontmatter — which Render
// drops with a reported Skip).
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP from .cursor/mcp.json (mcpServers — same shape as Claude).
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

	warn := a.stderr()

	// Skills from .cursor/skills/<name>/ (SKILL.md + bundled files).
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
			fm, body, lenient, err := claude.ParseFrontmatterWithReport(data)
			if err != nil {
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

	// Subagents from .cursor/agents/<name>.md.
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
			fm, body, lenient, err := claude.ParseFrontmatterWithReport(data)
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

	// Commands from .cursor/commands/<name>.md. Cursor commands are plain
	// markdown; ParseFrontmatterWithReport returns an empty frontmatter map and
	// the whole file as body when there is no `---` fence (the common case, since
	// Render writes body-only), and still captures any frontmatter a user did add.
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
			c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: fm, Body: body})
		}
	}

	// Hooks from .cursor/hooks.json (camelCase events mapped back to canonical).
	if data, err := os.ReadFile(p.Hooks); err == nil {
		var top map[string]any
		if json.Unmarshal(data, &top) == nil {
			c.Hooks = append(c.Hooks, ingestHooks(top["hooks"])...)
		}
	}

	// Memory from AGENTS.md (project scope only — user-scope rules live in
	// Cursor's app-local storage, so p.Memory is empty at user scope).
	if p.Memory != "" {
		if data, err := os.ReadFile(p.Memory); err == nil {
			c.Memory.Body = string(data)
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
