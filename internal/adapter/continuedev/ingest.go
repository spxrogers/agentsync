package continuedev

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest reads Continue's native block files and returns a partial
// source.Canonical. It is the inverse of Render (modulo the documented projected
// loss — command argument-hint/allowed-tools — which Render drops with a report).
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP from .continue/mcpServers/*.yaml (one or more server entries per block).
	if entries, err := os.ReadDir(p.MCPDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := filepath.Ext(e.Name())
			if ext != ".yaml" && ext != ".yml" && ext != ".json" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.MCPDir, e.Name()))
			if err != nil {
				continue
			}
			var block map[string]any
			if yaml.Unmarshal(data, &block) != nil {
				continue
			}
			servers, ok := block["mcpServers"].([]any)
			if !ok {
				continue
			}
			fallbackID := e.Name()[:len(e.Name())-len(ext)]
			for _, raw := range servers {
				srv, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				id := asStr(srv["name"])
				if id == "" {
					id = fallbackID
				}
				c.MCPServers = append(c.MCPServers, source.MCPServer{ID: id, Server: IngestMCPSpec(srv)})
			}
		}
	}

	warn := a.stderr()

	// Commands from .continue/prompts/<name>.md (prompt blocks). Only the
	// canonical-relevant `description` is captured; Continue-specific frontmatter
	// (name/invokable) is dropped so the round-trip stays clean.
	if entries, err := os.ReadDir(p.PromptsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.PromptsDir, e.Name()))
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
			cf := map[string]any{}
			if d, ok := fm["description"].(string); ok && d != "" {
				cf["description"] = d
			}
			c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: cf, Body: body})
		}
	}

	// Memory from .continue/rules/agentsync.md (the agentsync-owned always-apply
	// rule), captured verbatim.
	if data, err := os.ReadFile(filepath.Join(p.RulesDir, memoryRuleFile)); err == nil {
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
