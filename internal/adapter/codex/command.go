package codex

import (
	"fmt"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderCommands projects canonical slash commands into Codex custom prompts
// (~/.codex/prompts/<name>.md). Codex prompts are markdown with YAML
// frontmatter (description + argument-hint) and $1../$ARGUMENTS placeholders,
// so the projection is full-fidelity — EXCEPT they are global-only: Codex has
// no project-scoped prompts directory, so at project scope every command is
// skipped with a report rather than written somewhere Codex won't read it.
func (a *Adapter) renderCommands(c source.Canonical, p Paths, scope adapter.Scope) ([]adapter.FileOp, []adapter.Skip, error) {
	if scope == adapter.ScopeProject {
		var skips []adapter.Skip
		for _, cmd := range c.Commands {
			skips = append(skips, adapter.Skip{
				Component: "command",
				Name:      cmd.Name,
				Reason:    "Codex custom prompts are global-only; no project-scope target",
			})
		}
		return nil, skips, nil
	}
	var ops []adapter.FileOp
	for _, cmd := range c.Commands {
		body, err := claude.EncodeFrontmatter(cmd.Frontmatter, cmd.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("encode command %s: %w", cmd.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.PromptsDir, cmd.Name+".md"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("commands", cmd.Name+".md"),
			MergeStrategy: "replace",
		})
	}
	return ops, nil, nil
}
