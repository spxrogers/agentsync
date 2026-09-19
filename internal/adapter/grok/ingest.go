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
	"github.com/spxrogers/agentsync/internal/paths"
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
				s := IngestMCPSpec(spec)
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

// validateHome is the single gate every GROK_HOME-consuming path goes through
// (Detect, Render, Ingest, hooks, VersionRoots). It refuses three values that are
// never a legitimate Grok config directory and would make agentsync misbehave:
//
//   - a relative path — it would turn into writes relative to agentsync's cwd;
//   - the filesystem root — `/config.toml`, `/AGENTS.md`, `/skills/…`, and a
//     version root that swallows every other agent's dir;
//   - $HOME itself (the adapter's TargetRoot) — same collapse, plus agentsync
//     would `git init` the user's home directory, breaking the documented
//     invariant that it never inits a repo at $HOME. Compared with
//     paths.SameDirResolved, so a symlinked or (macOS/Windows) case-varied spelling
//     of $HOME is caught too, not just the byte-identical one.
//
// An absolute GROK_HOME anywhere else, inside or outside $HOME, is what upstream
// provides the variable for and is accepted here. An ANCESTOR of $HOME other than
// the root (`/home`, `/Users`) is not an error — that is a plausible if odd place
// to keep a config dir — but the apply tail's central never-at-or-above-$HOME
// guard (internal/cli enabledVersionRoots) drops it from git backup, with a
// warning, so the collapse cannot happen either way (issue #270).
//
// New cleans GrokHome, so the Clean here is defensive for an Adapter built
// without it. TargetRoot is paths.HomeDir and is never empty in production
// (registryFactory); the `.` check only keeps a zero Options from comparing
// against the cwd.
func (a *Adapter) validateHome() error {
	h := a.opts.GrokHome
	if h == "" {
		return nil
	}
	if !filepath.IsAbs(h) {
		return fmt.Errorf("GROK_HOME must be an absolute path")
	}
	h = filepath.Clean(h)
	home := filepath.Clean(a.opts.TargetRoot)
	suggest := "~/.grok"
	if home != "" && home != "." {
		suggest = filepath.Join(home, ".grok")
	}
	if h == filepath.VolumeName(h)+string(filepath.Separator) {
		return fmt.Errorf("GROK_HOME must not be the filesystem root (%s); point it at a directory such as %s", h, suggest)
	}
	if home != "" && home != "." && paths.SameDirResolved(h, home) {
		return fmt.Errorf("GROK_HOME must not be your home directory (%s); point it at a directory such as %s", h, suggest)
	}
	return nil
}
