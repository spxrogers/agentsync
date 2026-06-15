package gemini

import "path/filepath"

// Paths resolves the destination paths for a given (scope, project, target-root).
//
// settings.json holds both `mcpServers` and `hooks` (the adapter's single
// key-merge file). Memory is the hierarchical GEMINI.md context file:
// `~/.gemini/GEMINI.md` at user scope, repo-root `GEMINI.md` at project scope.
type Paths struct {
	ConfigDir   string // ~/.gemini (or <proj>/.gemini)
	Settings    string // .gemini/settings.json (mcpServers + hooks; merge-json-keys)
	Memory      string // ~/.gemini/GEMINI.md (user) or <proj>/GEMINI.md (project root)
	CommandsDir string // .gemini/commands (slash-command TOML)
	AgentsDir   string // .gemini/agents (subagent markdown)
}

// ResolvePaths returns the Paths for the given target root and optional project.
// projectScope=true + non-empty project uses project-local .gemini/ dirs and the
// repo-root GEMINI.md.
func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
	if projectScope && project != "" {
		cfg := filepath.Join(project, ".gemini")
		return Paths{
			ConfigDir:   cfg,
			Settings:    filepath.Join(cfg, "settings.json"),
			Memory:      filepath.Join(project, "GEMINI.md"),
			CommandsDir: filepath.Join(cfg, "commands"),
			AgentsDir:   filepath.Join(cfg, "agents"),
		}
	}
	cfg := filepath.Join(targetRoot, ".gemini")
	return Paths{
		ConfigDir:   cfg,
		Settings:    filepath.Join(cfg, "settings.json"),
		Memory:      filepath.Join(cfg, "GEMINI.md"),
		CommandsDir: filepath.Join(cfg, "commands"),
		AgentsDir:   filepath.Join(cfg, "agents"),
	}
}
