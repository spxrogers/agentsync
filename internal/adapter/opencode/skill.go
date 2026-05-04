package opencode

import (
	"fmt"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderSkills writes each skill to the shared .claude/skills/<name>/SKILL.md
// path that OpenCode reads natively (same as Claude). When both adapters are
// active, render.Apply dedupes per-path so the file is written once.
func (a *Adapter) renderSkills(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	var ops []adapter.FileOp
	for _, s := range c.Skills {
		body, err := claude.EncodeFrontmatter(s.Frontmatter, s.Body)
		if err != nil {
			return nil, fmt.Errorf("encode skill %s: %w", s.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.ClaudeSkillsDir, s.Name, "SKILL.md"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("skills", s.Name, "SKILL.md"),
			MergeStrategy: "replace",
		})
	}
	return ops, nil
}
