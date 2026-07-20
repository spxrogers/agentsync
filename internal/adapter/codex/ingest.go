package codex

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// Ingest reads Codex's native config files and returns a partial
// source.Canonical. It is the inverse of Render.
//
// Round-trip note: subagents lose the Claude-side `tools`/`color` frontmatter
// (Codex agents have no equivalent), so Ingest reconstructs the `name` (required
// by Codex, re-populated from the TOML `name` field so a frontmatter name that
// deliberately diverges from the file stem survives the round-trip),
// `description`, and `model` that were written to the agent TOML, plus the body
// from `developer_instructions`. Project-scope slash commands are never written
// (global-only), so they don't ingest at project scope either.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP ([mcp_servers.<id>]) and hooks ([hooks.<event>]) both live in
	// config.toml, so parse it once. A corrupt-but-present config.toml fails
	// loudly (parse error returned) rather than silently reading as "no MCP, no
	// hooks", which drift could misclassify as both components cleared.
	if data, present, err := adapter.ReadFileOptional(p.Config); err != nil {
		return c, fmt.Errorf("read %s: %w", p.Config, err)
	} else if present {
		var top map[string]any
		if err := toml.Unmarshal(data, &top); err != nil {
			return c, fmt.Errorf("parse %s: %w", p.Config, err)
		}
		if servers, ok := top["mcp_servers"].(map[string]any); ok {
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

	// Skills from ~/.agents/skills/<name>/ (SKILL.md + bundled files)
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

	// Subagents from ~/.codex/agents/<name>.toml (TOML → frontmatter + body)
	agentEntries, present, err := adapter.ReadDirOptional(p.AgentsDir)
	if err != nil {
		return c, fmt.Errorf("read agents dir %s: %w", p.AgentsDir, err)
	}
	if present {
		for _, e := range agentEntries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
				continue
			}
			name := e.Name()[:len(e.Name())-len(".toml")]
			data, err := os.ReadFile(filepath.Join(p.AgentsDir, e.Name()))
			if err != nil {
				if !os.IsNotExist(err) {
					fmt.Fprintf(warn, "warning: skipping subagent %q: read: %v\n", name, err)
				}
				continue
			}
			var af codexAgentFile
			if err := toml.Unmarshal(data, &af); err != nil {
				fmt.Fprintf(warn, "warning: skipping subagent %q: %v\n", name, err)
				continue
			}
			fm := map[string]any{}
			// Re-populate the frontmatter `name` from the TOML `name` field
			// unconditionally on presence (even when it equals the file stem):
			// this makes the round-trip lossless for a deliberately-divergent name
			// and a harmless no-op for a matching one, since Render prefers
			// Frontmatter["name"] when present and falls back to the canonical
			// (filename-derived) name otherwise.
			if af.Name != "" {
				fm["name"] = af.Name
			}
			if af.Description != "" {
				fm["description"] = af.Description
			}
			if af.Model != "" {
				fm["model"] = af.Model
			}
			// The canonical Name stays derived from the filename — it is the
			// on-disk identity / SourceID stem — while the frontmatter carries the
			// TOML `name` value above.
			c.Subagents = append(c.Subagents, source.Subagent{Name: name, Frontmatter: fm, Body: af.DeveloperInstructions})
		}
	}

	// Commands from ~/.codex/prompts/<name>.md. Codex prompts are global-only, so
	// render writes them at user scope ONLY; mirror that here so a stray
	// <project>/.codex/prompts/ (which Codex ignores) is not captured as a
	// phantom project-scope command that apply would never write back.
	if scope == adapter.ScopeUser {
		promptEntries, present, err := adapter.ReadDirOptional(p.PromptsDir)
		if err != nil {
			return c, fmt.Errorf("read prompts dir %s: %w", p.PromptsDir, err)
		}
		if present {
			for _, e := range promptEntries {
				if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
					continue
				}
				name := e.Name()[:len(e.Name())-len(".md")]
				data, err := os.ReadFile(filepath.Join(p.PromptsDir, e.Name()))
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
	}

	// Memory from AGENTS.md (managed-file banner stripped — see claude/ingest.go)
	if data, present, err := adapter.ReadFileOptional(p.Memory); err != nil {
		return c, fmt.Errorf("read memory %s: %w", p.Memory, err)
	} else if present {
		c.Memory.Body = source.StripManagedBanner(string(data))
	}

	return c, nil
}

// ingestHooks decodes config.toml's [hooks.<event>] tables (the value of the
// top-level "hooks" key) into canonical hooks. The TOML decode yields the same
// map shape as the JSON Codex/Claude hook schema (event → []{matcher, hooks:
// [{type, command}]}), so the walk is format-agnostic. Inverse of renderHooks.
func ingestHooks(raw any) []source.Hook {
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	var out []source.Hook
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
				out = append(out, source.Hook{
					Event:   untrusted.Wrap(event), // native config map key
					Matcher: matcher,
					Type:    asStr(h["type"]),
					Command: asStr(h["command"]),
				})
			}
		}
	}
	return out
}
