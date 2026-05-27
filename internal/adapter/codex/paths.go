package codex

import "path/filepath"

// Paths resolves the destination paths for a given (scope, project, target-root).
//
// Note SkillsDir lives under ~/.agents/skills (the cross-agent AGENTS skills
// directory Codex scans), NOT under ~/.codex — Codex reads personal skills from
// $HOME/.agents/skills. Everything else is rooted at $CODEX_HOME (~/.codex).
type Paths struct {
	ConfigDir  string // ~/.codex
	Config     string // ~/.codex/config.toml (mcp_servers + [hooks.*] + plugin enables live here)
	Memory     string // AGENTS.md path
	SkillsDir  string // ~/.agents/skills (shared cross-agent skills dir)
	AgentsDir  string // ~/.codex/agents (subagent TOML)
	PromptsDir string // ~/.codex/prompts (custom prompts → slash commands)
}

// ResolvePaths returns the Paths for the given target root and optional project.
// projectScope=true + non-empty project uses project-local .codex/ dirs and the
// repo's AGENTS.md / .agents/skills.
func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
	if projectScope && project != "" {
		cfg := filepath.Join(project, ".codex")
		return Paths{
			ConfigDir:  cfg,
			Config:     filepath.Join(cfg, "config.toml"),
			Memory:     filepath.Join(project, "AGENTS.md"),
			SkillsDir:  filepath.Join(project, ".agents", "skills"),
			AgentsDir:  filepath.Join(cfg, "agents"),
			PromptsDir: filepath.Join(cfg, "prompts"),
		}
	}
	cfg := filepath.Join(targetRoot, ".codex")
	return Paths{
		ConfigDir:  cfg,
		Config:     filepath.Join(cfg, "config.toml"),
		Memory:     filepath.Join(cfg, "AGENTS.md"),
		SkillsDir:  filepath.Join(targetRoot, ".agents", "skills"),
		AgentsDir:  filepath.Join(cfg, "agents"),
		PromptsDir: filepath.Join(cfg, "prompts"),
	}
}
