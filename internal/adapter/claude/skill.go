package claude

import (
	"fmt"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

func (a *Adapter) renderSkills(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	var ops []adapter.FileOp
	for _, s := range c.Skills {
		body, err := EncodeFrontmatter(s.Frontmatter, s.Body)
		if err != nil {
			return nil, fmt.Errorf("encode skill %s: %w", s.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.SkillsDir, s.Name, "SKILL.md"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("skills", s.Name, "SKILL.md"),
			MergeStrategy: "replace",
		})
	}
	return ops, nil
}
