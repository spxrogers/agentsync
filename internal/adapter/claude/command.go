package claude

import (
	"fmt"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

func (a *Adapter) renderCommands(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	var ops []adapter.FileOp
	for _, cmd := range c.Commands {
		body, err := EncodeFrontmatter(cmd.Frontmatter, cmd.Body)
		if err != nil {
			return nil, fmt.Errorf("encode command %s: %w", cmd.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.CommandsDir, cmd.Name+".md"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("commands", cmd.Name+".md"),
			MergeStrategy: "replace",
		})
	}
	return ops, nil
}
