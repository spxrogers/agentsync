package cursor

import "path/filepath"

// Paths resolves the destination paths for a given (scope, project, target-root).
//
// Note SkillsDir lives under ~/.cursor/skills (Cursor reads personal skills from
// $HOME/.cursor/skills as well as the shared ~/.agents/skills). Memory is empty
// at user scope: Cursor keeps user-level rules in app-local storage, so there is
// no user-scope filesystem target — only project-scope memory lands as the
// repo-root AGENTS.md (which Cursor reads natively).
type Paths struct {
	ConfigDir   string // ~/.cursor (or <proj>/.cursor)
	MCP         string // .cursor/mcp.json  (mcpServers; merge-json-keys)
	Hooks       string // .cursor/hooks.json ({version, hooks}; merge-json-keys)
	Memory      string // <proj>/AGENTS.md at project scope; "" at user scope
	SkillsDir   string // .cursor/skills (skill directories)
	AgentsDir   string // .cursor/agents (subagent markdown)
	CommandsDir string // .cursor/commands (slash-command markdown)
}

// ResolvePaths returns the Paths for the given target root and optional project.
// projectScope=true + non-empty project uses project-local .cursor/ dirs and the
// repo's AGENTS.md.
func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
	if projectScope && project != "" {
		cfg := filepath.Join(project, ".cursor")
		return Paths{
			ConfigDir:   cfg,
			MCP:         filepath.Join(cfg, "mcp.json"),
			Hooks:       filepath.Join(cfg, "hooks.json"),
			Memory:      filepath.Join(project, "AGENTS.md"),
			SkillsDir:   filepath.Join(cfg, "skills"),
			AgentsDir:   filepath.Join(cfg, "agents"),
			CommandsDir: filepath.Join(cfg, "commands"),
		}
	}
	cfg := filepath.Join(targetRoot, ".cursor")
	return Paths{
		ConfigDir:   cfg,
		MCP:         filepath.Join(cfg, "mcp.json"),
		Hooks:       filepath.Join(cfg, "hooks.json"),
		Memory:      "", // user-level rules live in Cursor's app-local storage
		SkillsDir:   filepath.Join(cfg, "skills"),
		AgentsDir:   filepath.Join(cfg, "agents"),
		CommandsDir: filepath.Join(cfg, "commands"),
	}
}
