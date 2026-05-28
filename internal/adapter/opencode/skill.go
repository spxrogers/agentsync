package opencode

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderSkills writes each skill (SKILL.md plus its bundled scripts/references/
// assets) to the shared .claude/skills/<name>/ tree that OpenCode reads
// natively (same as Claude). When both adapters are active, render.Apply dedupes
// per-path so each file is written once.
func (a *Adapter) renderSkills(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	return claude.SkillFileOps(c.Skills, p.ClaudeSkillsDir)
}
