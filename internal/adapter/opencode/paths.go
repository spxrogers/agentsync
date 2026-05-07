package opencode

import "path/filepath"

// Paths resolves the destination paths for a given (scope, project, target-root).
type Paths struct {
	ConfigDir       string // ~/.config/opencode
	Settings        string // ~/.config/opencode/opencode.json
	AgentsDir       string // ~/.config/opencode/agents (user) or .opencode/agents (project)
	CommandsDir     string // ~/.config/opencode/commands (user) or .opencode/commands (project)
	ClaudeSkillsDir string // ~/.claude/skills (shared with Claude!)
	Memory          string // AGENTS.md path
}

// ResolvePaths returns the Paths for the given target root and optional project.
// projectScope=true + non-empty project uses project-local .opencode/ dirs.
func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
	if projectScope && project != "" {
		return Paths{
			ConfigDir:       filepath.Join(project, ".opencode"),
			Settings:        filepath.Join(project, ".opencode", "opencode.json"),
			AgentsDir:       filepath.Join(project, ".opencode", "agents"),
			CommandsDir:     filepath.Join(project, ".opencode", "commands"),
			ClaudeSkillsDir: filepath.Join(project, ".claude", "skills"),
			Memory:          filepath.Join(project, "AGENTS.md"),
		}
	}
	cfg := filepath.Join(targetRoot, ".config", "opencode")
	return Paths{
		ConfigDir:       cfg,
		Settings:        filepath.Join(cfg, "opencode.json"),
		AgentsDir:       filepath.Join(cfg, "agents"),
		CommandsDir:     filepath.Join(cfg, "commands"),
		ClaudeSkillsDir: filepath.Join(targetRoot, ".claude", "skills"),
		Memory:          filepath.Join(cfg, "AGENTS.md"),
	}
}
