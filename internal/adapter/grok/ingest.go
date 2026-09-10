package grok

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest captures the adapter's native destinations. Compatibility-vendor
// trees, extra search roots, and other hooks/*.json files are left alone.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	var c source.Canonical
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return c, err
	}
	if err := a.validateHome(); err != nil {
		return c, err
	}
	p := a.resolvePaths(scope, project)
	data, present, err := adapter.ReadFileOptional(p.Config)
	if err != nil {
		return c, fmt.Errorf("read %s: %w", p.Config, err)
	}
	if present {
		var top map[string]any
		if err := toml.Unmarshal(data, &top); err != nil {
			return c, fmt.Errorf("parse %s: %w", p.Config, err)
		}
		if raw, present := top["mcp_servers"]; present {
			servers, ok := raw.(map[string]any)
			if !ok {
				return c, fmt.Errorf("parse %s: mcp_servers must be a table", p.Config)
			}
			for id, raw := range servers {
				spec, ok := raw.(map[string]any)
				if !ok {
					return c, fmt.Errorf("parse %s: MCP server %q must be a table", p.Config, id)
				}
				s, err := IngestMCPSpec(spec)
				if err != nil {
					return c, fmt.Errorf("parse MCP server %q: %w", id, err)
				}
				c.MCPServers = append(c.MCPServers, source.MCPServer{ID: id, Server: s})
			}
		}
	}
	slices.SortFunc(c.MCPServers, func(a, b source.MCPServer) int { return strings.Compare(a.ID, b.ID) })
	if data, present, err := adapter.ReadFileOptional(p.Memory); err != nil {
		return c, fmt.Errorf("read memory %s: %w", p.Memory, err)
	} else if present {
		c.Memory.Body = source.StripManagedBanner(string(data))
	}
	entries, _, err := adapter.ReadDirOptional(p.SkillsDir)
	if err != nil {
		return c, fmt.Errorf("read skills %s: %w", p.SkillsDir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(p.SkillsDir, entry.Name())
		fm, body, present, err := a.readMarkdown(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			return c, err
		}
		if !present {
			continue
		}
		files, err := source.ReadSkillFiles(afero.NewOsFs(), dir)
		if err != nil {
			return c, fmt.Errorf("read skill %q bundled files: %w", entry.Name(), err)
		}
		c.Skills = append(c.Skills, source.Skill{Name: entry.Name(), Frontmatter: fm, Body: body, Files: files})
	}
	entries, _, err = adapter.ReadDirOptional(p.CommandsDir)
	if err != nil {
		return c, fmt.Errorf("read commands %s: %w", p.CommandsDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			fmt.Fprintf(a.stderr(), "warning: nested Grok command directory %q is not captured\n", entry.Name())
			continue
		}
		if filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		fm, body, present, err := a.readMarkdown(filepath.Join(p.CommandsDir, entry.Name()))
		if err != nil {
			return c, err
		}
		if present {
			c.Commands = append(c.Commands, source.Command{Name: strings.TrimSuffix(entry.Name(), ".md"), Frontmatter: fm, Body: body})
		}
	}
	c.Hooks, _, err = readHooks(p.Hooks, a.stderr())
	if err != nil {
		return c, err
	}
	entries, _, err = adapter.ReadDirOptional(filepath.Dir(p.Hooks))
	if err != nil {
		return c, fmt.Errorf("read hooks directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" && entry.Name() != filepath.Base(p.Hooks) {
			fmt.Fprintf(a.stderr(), "warning: Grok hook file %q is not captured; only hooks/agentsync.json is managed to avoid duplicate execution\n", entry.Name())
		}
	}
	return c, nil
}

func (a *Adapter) readMarkdown(path string) (map[string]any, string, bool, error) {
	data, present, err := adapter.ReadFileOptional(path)
	if err != nil || !present {
		return nil, "", false, err
	}
	fm, body, lenient, err := claude.ParseFrontmatterWithReport(data)
	if err != nil {
		return nil, "", false, fmt.Errorf("parse %s: %w", path, err)
	}
	if lenient {
		fmt.Fprintf(a.stderr(), "warning: %q frontmatter parsed leniently; consider quoting YAML values\n", path)
	}
	return fm, body, true, nil
}

// Never turn a relative override into writes relative to agentsync's cwd.
func (a *Adapter) validateHome() error {
	if a.opts.GrokHome != "" && !filepath.IsAbs(a.opts.GrokHome) {
		return fmt.Errorf("GROK_HOME must be an absolute path")
	}
	return nil
}
