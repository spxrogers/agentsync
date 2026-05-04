package claude

import (
	"fmt"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

func (a *Adapter) renderSubagents(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	var ops []adapter.FileOp
	for _, s := range c.Subagents {
		body, err := EncodeFrontmatter(s.Frontmatter, s.Body)
		if err != nil {
			return nil, fmt.Errorf("encode subagent %s: %w", s.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.AgentsDir, s.Name+".md"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("agents", s.Name+".md"),
			MergeStrategy: "replace",
		})
	}
	return ops, nil
}
