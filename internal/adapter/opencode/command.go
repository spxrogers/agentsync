package opencode

import (
	"fmt"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderCommands translates Claude-shaped command frontmatter to OpenCode-shaped
// and emits FileOps for .config/opencode/commands/<name>.md.
//
// Frontmatter mapping:
//
//	description   -> description  (direct copy)
//	model         -> model        (direct copy)
//	argument-hint -> drop + Skip  (no OpenCode equivalent)
//
// Body is preserved as-is; OpenCode treats the body as the command template.
// We do NOT add a `template:` frontmatter key to avoid duplication.
func (a *Adapter) renderCommands(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, cmd := range c.Commands {
		out := map[string]any{}
		if v, ok := cmd.Frontmatter["description"]; ok {
			out["description"] = v
		}
		if v, ok := cmd.Frontmatter["model"]; ok {
			out["model"] = v
		}

		// Drop unmappable fields, emit Skip notes.
		if _, ok := cmd.Frontmatter["argument-hint"]; ok {
			skips = append(skips, adapter.Skip{
				Component: "command-frontmatter", Name: cmd.Name,
				Reason: "Claude `argument-hint` has no OpenCode equivalent",
			})
		}

		body, err := claude.EncodeFrontmatter(out, cmd.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("encode opencode command %s: %w", cmd.Name, err)
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
	return ops, skips, nil
}
