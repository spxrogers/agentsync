package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/tailscale/hujson"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
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
						m := source.MCPServer{ID: id, Server: IngestMCPSpec(spec)}
						c.MCPServers = append(c.MCPServers, m)
					}
				}
			}
		}
	}

	// Skills from ~/.claude/skills/<name>/ (SKILL.md + bundled files; shared with Claude)
	if entries, err := os.ReadDir(p.ClaudeSkillsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillDir := filepath.Join(p.ClaudeSkillsDir, e.Name())
			data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
			if err != nil {
				continue
			}
			fm, body, err := claude.ParseFrontmatter(data)
			if err != nil {
				continue
			}
			files, err := source.ReadSkillFiles(afero.NewOsFs(), skillDir)
			if err != nil {
				continue
			}
			c.Skills = append(c.Skills, source.Skill{Name: e.Name(), Frontmatter: fm, Body: body, Files: files})
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

// IngestMCPSpec translates one OpenCode-native MCP server spec — the value
// under opencode.json `/mcp/<id>` — into the canonical MCPServerSpec. It is the
// exact inverse of opencodeMCPSpec (Render). Exported so reconcile's key-level
// write-back reconstructs an opencode MCP server through the SAME translation
// the adapter uses, rather than assuming the dest shape matches the canonical
// model (it doesn't: command is an array, env is `environment`, type is
// local/remote).
func IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	command, args := opencodeIngestCommand(raw["command"])
	return source.MCPServerSpec{
		Type:    canonicalMCPType(asStr(raw["type"])),
		Command: command,
		Args:    args,
		Env:     asStrMap(raw["environment"]),
		URL:     asStr(raw["url"]),
		Headers: asStrMap(raw["headers"]),
	}
}

// canonicalMCPType maps OpenCode's transport ("local"/"remote") back to the
// canonical model's stdio/http. OpenCode has no separate sse transport, so a
// remote server normalises to "http" on write-back (an apply-only flow is
// unaffected — render maps both http and sse to "remote"). An unrecognised
// value is preserved verbatim.
func canonicalMCPType(opencodeType string) string {
	switch opencodeType {
	case "local":
		return "stdio"
	case "remote":
		return "http"
	default:
		return opencodeType
	}
}

// opencodeIngestCommand splits OpenCode's command — a string array
// [command, ...args] — into the canonical command + args. A bare string is
// tolerated for hand-edited configs. A single-element array yields no args so
// the value round-trips cleanly (render flattens command+nil-args back to it).
func opencodeIngestCommand(v any) (command string, args []string) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	arr := asStrSlice(v)
	switch len(arr) {
	case 0:
		return "", nil
	case 1:
		return arr[0], nil
	default:
		return arr[0], arr[1:]
	}
}

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
