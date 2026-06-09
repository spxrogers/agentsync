package gemini

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest reads Gemini CLI's native config files and returns a partial
// source.Canonical. It is the inverse of Render (modulo the documented projected
// loss — subagent tools/color, command argument-hint/allowed-tools — which Render
// drops with a reported Skip).
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP (mcpServers) and hooks (hooks) both live in settings.json — parse once.
	if data, err := os.ReadFile(p.Settings); err == nil {
		var top map[string]any
		if err := json.Unmarshal(data, &top); err != nil {
			return c, fmt.Errorf("parse %s: %w", p.Settings, err)
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
		c.Hooks = append(c.Hooks, ingestHooks(top["hooks"])...)
	}

	warn := a.stderr()

	// Commands from .gemini/commands/<name>.toml (TOML → description + body).
	if entries, err := os.ReadDir(p.CommandsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.CommandsDir, e.Name()))
			if err != nil {
				continue
			}
			var cf geminiCommandFile
			if toml.Unmarshal(data, &cf) != nil {
				continue
			}
			name := e.Name()[:len(e.Name())-len(".toml")]
			fm := map[string]any{}
			if cf.Description != "" {
				fm["description"] = cf.Description
			}
			c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: fm, Body: cf.Prompt})
		}
	}

	// Subagents from .gemini/agents/<name>.md (markdown → frontmatter + body).
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

	// Memory from GEMINI.md (verbatim).
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
