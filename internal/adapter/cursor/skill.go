package cursor

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderSkills writes each skill (SKILL.md plus its bundled scripts/references/
// assets) to `.cursor/skills/<name>/`. Cursor reads skills from this directory
// (and the shared ~/.agents/skills) using the same SKILL.md format Claude,
// OpenCode, and Codex use, so this is a full-fidelity projection of the whole
// skill directory.
func (a *Adapter) renderSkills(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	return claude.SkillFileOps(c.Skills, p.SkillsDir)
}
