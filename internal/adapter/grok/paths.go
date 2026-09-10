package grok

import (
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// Paths are Grok's native destinations at one scope.
type Paths struct {
	ConfigDir   string
	Config      string
	Memory      string
	SkillsDir   string
	CommandsDir string
	Hooks       string
}

func (a *Adapter) resolvePaths(scope adapter.Scope, project string) Paths {
	dir := filepath.Join(a.opts.TargetRoot, ".grok")
	if a.opts.GrokHome != "" {
		dir = a.opts.GrokHome
	}
	memory := filepath.Join(dir, "AGENTS.md")
	if scope == adapter.ScopeProject {
		dir = filepath.Join(project, ".grok")
		memory = filepath.Join(project, "AGENTS.md")
	}
	return Paths{
		ConfigDir: dir, Config: filepath.Join(dir, "config.toml"), Memory: memory,
		SkillsDir: filepath.Join(dir, "skills"), CommandsDir: filepath.Join(dir, "commands"),
		Hooks: filepath.Join(dir, "hooks", "agentsync.json"),
	}
}
