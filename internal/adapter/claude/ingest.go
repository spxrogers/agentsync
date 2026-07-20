package claude

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest reads native Claude config files and returns a partial source.Canonical.
// It is the inverse of Render: Ingest(Apply(Render(c))) round-trips to c
// for the components agentsync manages. Hook events settings.json holds that the
// canonical model cannot fully represent — an unmodeled definition/handler field
// (e.g. `timeout`) or a non-command handler type — are left uncaptured with a
// warning rather than captured as a lossy subset, matching the Gemini adapter:
// the next apply owns the whole per-event array, so capturing a subset would let
// it rewrite the user's native entry without those fields.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP from ~/.claude.json (user) or <proj>/.mcp.json (project — the file
	// `claude mcp add --scope project` writes; settings.json is never project MCP).
	// mcpDest centralizes the scope→file choice shared with renderMCP.
	mcpFile := p.mcpDest(scope)
	data, present, err := adapter.ReadFileOptional(mcpFile)
	if err != nil {
		return c, fmt.Errorf("read %s: %w", mcpFile, err)
	}
	if present {
		// Decode preserving json.Number (jsonkeys.DecodeObject, json.Decoder +
		// UseNumber) — the same rationale as apply.go's readJSONFile: an unmodeled
		// native key that flows into Extra (e.g. a timeout in nanoseconds) can hold
		// an integer > 2^53, which plain json.Unmarshal would round to float64 and
		// then re-marshal lossily on the next render. UseNumber keeps the literal
		// digits so Extra round-trips byte-exact.
		top, err := jsonkeys.DecodeObject(data)
		if err != nil {
			return c, fmt.Errorf("parse %s: %w", mcpFile, err)
		}
		if servers, ok := top["mcpServers"].(map[string]any); ok {
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
					Extra:   ExtraNativeKeys(spec, "type", "command", "args", "env", "url", "headers"),
				}}
				c.MCPServers = append(c.MCPServers, m)
			}
		}
	}

	warn := a.stderr()

	// Skills from ~/.claude/skills/<name>/ (SKILL.md + bundled files)
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
				// A directory with no SKILL.md is not a skill — skip silently.
				// A present-but-unreadable SKILL.md is surfaced (never a silent
				// drop) so the user can fix it, matching the parse-error warning.
				if !os.IsNotExist(err) {
					fmt.Fprintf(warn, "warning: skipping skill %q: read SKILL.md: %v\n", e.Name(), err)
				}
				continue
			}
			fm, body, lenient, err := ParseFrontmatterWithReport(data)
			if err != nil {
				// A parse error used to silently drop the skill. Warn so the
				// user knows their skill is being skipped and can fix the file.
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

	// Subagents from ~/.claude/agents/<name>.md
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
			fm, body, lenient, err := ParseFrontmatterWithReport(data)
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

	// Commands from ~/.claude/commands/<name>.md
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
			fm, body, lenient, err := ParseFrontmatterWithReport(data)
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

	// Hooks from settings.json /hooks/<event>. A corrupt-but-present settings.json
	// now fails loudly (parse error returned), matching the MCP block above and
	// removing the old MCP-loud / hooks-silent asymmetry. Decode with UseNumber
	// (jsonkeys.DecodeObject) so an unmodeled large integer survives as json.Number
	// rather than a rounded float64 (same precision reason as the MCP block +
	// apply.go), for defense-in-depth consistency across all three decode sites.
	settingsData, present, err := adapter.ReadFileOptional(p.Settings)
	if err != nil {
		return c, fmt.Errorf("read %s: %w", p.Settings, err)
	}
	if present {
		top, err := jsonkeys.DecodeObject(settingsData)
		if err != nil {
			return c, fmt.Errorf("parse %s: %w", p.Settings, err)
		}
		// ingestHooks takes any and does its own map assertion (mirroring the
		// gemini adapter's call site), so pass the raw value straight through.
		c.Hooks = append(c.Hooks, ingestHooks(top["hooks"], warn)...)
	}

	// Memory from CLAUDE.md. The agentsync managed-file banner is a render-time
	// decoration with no canonical home, so it is stripped here (like Windsurf's
	// activation frontmatter) — ingest yields the canonical model, which never
	// carries the banner. Fragment markers, by contrast, are STRUCTURAL signal
	// kept verbatim; their reverse-collapse into AGENTS.md + fragment files
	// happens in the write-back layer (source.CollapseMemoryMarkers).
	memData, present, err := adapter.ReadFileOptional(p.Memory)
	if err != nil {
		return c, fmt.Errorf("read memory %s: %w", p.Memory, err)
	}
	if present {
		c.Memory.Body = source.StripManagedBanner(string(memData))
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
