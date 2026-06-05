package claude

import (
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// Paths resolves the destination paths for a given (scope, project, target-root).
type Paths struct {
	Home            string // ~/.claude
	Settings        string // ~/.claude/settings.json
	DotClaude       string // ~/.claude.json (user-scope mcpServers + plugin enables live here)
	MCPProject      string // <proj>/.mcp.json (project-scope mcpServers; empty at user scope)
	SkillsDir       string // ~/.claude/skills
	AgentsDir       string // ~/.claude/agents
	CommandsDir     string // ~/.claude/commands
	Memory          string // ~/.claude/CLAUDE.md (user scope) or <proj>/CLAUDE.md (project scope)
	PluginsCacheDir string // ~/.claude/plugins/cache
}

func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
	home := filepath.Join(targetRoot, ".claude")
	p := Paths{
		Home:            home,
		Settings:        filepath.Join(home, "settings.json"),
		DotClaude:       filepath.Join(targetRoot, ".claude.json"),
		SkillsDir:       filepath.Join(home, "skills"),
		AgentsDir:       filepath.Join(home, "agents"),
		CommandsDir:     filepath.Join(home, "commands"),
		Memory:          filepath.Join(home, "CLAUDE.md"),
		PluginsCacheDir: filepath.Join(home, "plugins", "cache"),
	}
	if projectScope && project != "" {
		// project-scope settings live under <project>/.claude/, but project-scope
		// MCP servers live in <project>/.mcp.json at the repo root — the file
		// `claude mcp add --scope project` writes and the team checks in (per
		// https://code.claude.com/docs/ MCP scopes). settings.json holds
		// hooks/LSP/permissions, never project MCP.
		projHome := filepath.Join(project, ".claude")
		p.Home = projHome
		p.Settings = filepath.Join(projHome, "settings.json")
		p.MCPProject = filepath.Join(project, ".mcp.json")
		p.SkillsDir = filepath.Join(projHome, "skills")
		p.AgentsDir = filepath.Join(projHome, "agents")
		p.CommandsDir = filepath.Join(projHome, "commands")
		p.Memory = filepath.Join(project, "CLAUDE.md")
	}
	return p
}

// mcpDest returns the destination file for MCP servers at the given scope: the
// user-global ~/.claude.json, or a project's repo-root .mcp.json. Centralizing
// the scope→file choice keeps renderMCP and Ingest from drifting apart on where
// project MCP lives. It would return "" for ScopeProject with no project root
// (MCPProject unset), but Render and Ingest reject that case up front via
// adapter.RequireProjectRoot, so callers never observe an empty result.
func (p Paths) mcpDest(scope adapter.Scope) string {
	if scope == adapter.ScopeProject {
		return p.MCPProject
	}
	return p.DotClaude
}
