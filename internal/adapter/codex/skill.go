package codex

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderSkills writes each skill (SKILL.md plus its bundled scripts/references/
// assets) to ~/.agents/skills/<name>/. Codex reads personal skills from the
// shared cross-agent ~/.agents/skills directory (not under ~/.codex), with the
// same SKILL.md format Claude and OpenCode use, so this is a full-fidelity
// projection of the whole skill directory.
func (a *Adapter) renderSkills(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	return claude.SkillFileOps(c.Skills, p.SkillsDir)
}
