package cursor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
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
	if data, present, err := adapter.ReadFileOptional(p.MCP); err != nil {
		return c, fmt.Errorf("read %s: %w", p.MCP, err)
	} else if present {
		// Decode with UseNumber (jsonkeys.DecodeObject) so an unmodeled large
		// integer in the Extra passthrough survives as json.Number rather than
		// a rounded float64 — same precision contract as the claude/gemini/
		// generic ingests; capture normalizes json.Number at its funnel.
		top, err := jsonkeys.DecodeObject(data)
		if err != nil {
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
	skillEntries, present, err := adapter.ReadDirOptional(p.SkillsDir)
	if err != nil {
		return c, fmt.Errorf("read skills dir %s: %w", p.SkillsDir, err)
	}
	if present {
		for _, e := range skillEntries {
			if !e.IsDir() {
				continue
			}
			skillDir := filepath.Join(p.SkillsDir, e.Name())
			data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
			if err != nil {
				if !os.IsNotExist(err) {
					fmt.Fprintf(warn, "warning: skipping skill %q: read SKILL.md: %v\n", e.Name(), err)
				}
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
	agentEntries, present, err := adapter.ReadDirOptional(p.AgentsDir)
	if err != nil {
		return c, fmt.Errorf("read agents dir %s: %w", p.AgentsDir, err)
	}
	if present {
		for _, e := range agentEntries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			name := e.Name()[:len(e.Name())-len(".md")]
			data, err := os.ReadFile(filepath.Join(p.AgentsDir, e.Name()))
			if err != nil {
				if !os.IsNotExist(err) {
					fmt.Fprintf(warn, "warning: skipping subagent %q: read: %v\n", name, err)
				}
				continue
			}
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
	commandEntries, present, err := adapter.ReadDirOptional(p.CommandsDir)
	if err != nil {
		return c, fmt.Errorf("read commands dir %s: %w", p.CommandsDir, err)
	}
	if present {
		for _, e := range commandEntries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			name := e.Name()[:len(e.Name())-len(".md")]
			data, err := os.ReadFile(filepath.Join(p.CommandsDir, e.Name()))
			if err != nil {
				if !os.IsNotExist(err) {
					fmt.Fprintf(warn, "warning: skipping command %q: read: %v\n", name, err)
				}
				continue
			}
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

	// Hooks from .cursor/hooks.json (camelCase events mapped back to canonical;
	// unrepresentable events are skipped with a warning — see ingestHooks). A
	// corrupt-but-present hooks.json fails loudly rather than reading as "no
	// hooks", matching the MCP block above.
	if data, present, err := adapter.ReadFileOptional(p.Hooks); err != nil {
		return c, fmt.Errorf("read %s: %w", p.Hooks, err)
	} else if present {
		// UseNumber decode for consistency with the MCP block above (hook fields
		// agentsync captures are all strings, but an unmodeled-key refusal must
		// see the same value shapes every other decode site sees).
		top, err := jsonkeys.DecodeObject(data)
		if err != nil {
			return c, fmt.Errorf("parse %s: %w", p.Hooks, err)
		}
		c.Hooks = append(c.Hooks, ingestHooks(top["hooks"], warn)...)
	}

	// Memory from AGENTS.md (project scope only — user-scope rules live in
	// Cursor's app-local storage, so p.Memory is empty at user scope).
	if p.Memory != "" {
		if data, present, err := adapter.ReadFileOptional(p.Memory); err != nil {
			return c, fmt.Errorf("read memory %s: %w", p.Memory, err)
		} else if present {
			c.Memory.Body = source.StripManagedBanner(string(data)) // banner stripped — see claude/ingest.go
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
