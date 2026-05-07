package claude

import "path/filepath"

// Paths resolves the destination paths for a given (scope, project, target-root).
type Paths struct {
	Home            string // ~/.claude
	Settings        string // ~/.claude/settings.json
	DotClaude       string // ~/.claude.json (mcpServers + plugin enables live here)
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
		// project-scope settings live under <project>/.claude/
		projHome := filepath.Join(project, ".claude")
		p.Home = projHome
		p.Settings = filepath.Join(projHome, "settings.json")
		p.SkillsDir = filepath.Join(projHome, "skills")
		p.AgentsDir = filepath.Join(projHome, "agents")
		p.CommandsDir = filepath.Join(projHome, "commands")
		p.Memory = filepath.Join(project, "CLAUDE.md")
	}
	return p
}
