package claude

import (
	"fmt"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

func (a *Adapter) renderSkills(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	return SkillFileOps(c.Skills, p.SkillsDir)
}

// SkillFileOps projects each skill into FileOps under skillsDir: the SKILL.md
// (frontmatter + body) plus one verbatim op per bundled file (scripts/,
// references/, assets/, …), preserving each bundled file's mode. It is shared
// by every adapter that writes skills to a skills directory (Claude, OpenCode,
// Codex) so the "a skill is a directory, not just SKILL.md" projection stays
// identical across them — when two adapters target the same skills dir, the
// render pipeline dedupes the byte-identical ops per path.
func SkillFileOps(skills []source.Skill, skillsDir string) ([]adapter.FileOp, error) {
	var ops []adapter.FileOp
	for _, s := range skills {
		body, err := EncodeFrontmatter(s.Frontmatter, s.Body)
		if err != nil {
			return nil, fmt.Errorf("encode skill %s: %w", s.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(skillsDir, s.Name, "SKILL.md"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("skills", s.Name, "SKILL.md"),
			MergeStrategy: "replace",
		})
		for _, f := range s.Files {
			mode := f.Mode
			if mode == 0 {
				mode = 0o644
			}
			ops = append(ops, adapter.FileOp{
				Action:        "write",
				Path:          filepath.Join(skillsDir, s.Name, filepath.FromSlash(f.Path)),
				Content:       f.Content,
				Mode:          mode,
				SourceID:      filepath.Join("skills", s.Name, filepath.FromSlash(f.Path)),
				MergeStrategy: "replace",
			})
		}
	}
	return ops, nil
}
