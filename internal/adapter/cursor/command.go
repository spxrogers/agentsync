package cursor

import (
	"path/filepath"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderCommands projects canonical slash commands into Cursor commands
// (`.cursor/commands/<name>.md`). Cursor commands are PLAIN MARKDOWN prompt
// files with no frontmatter (per cursor.com/docs) — the whole file is the
// prompt — so only the body survives: a command's `description`,
// `argument-hint`, and `allowed-tools` frontmatter has no Cursor home and is
// dropped with a reported Skip. Unlike Codex prompts (global-only), Cursor
// commands exist at BOTH user and project scope, so nothing is skipped on scope.
func (a *Adapter) renderCommands(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, cmd := range c.Commands {
		if len(cmd.Frontmatter) > 0 {
			skips = append(skips, adapter.Skip{
				Component: "command",
				Name:      cmd.Name,
				Reason: "Cursor commands are plain markdown (no frontmatter); dropped " +
					strings.Join(sortedKeys(cmd.Frontmatter), ", "),
				Kind: adapter.SkipReduced,
			})
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.CommandsDir, cmd.Name+".md"),
			Content:       []byte(cmd.Body),
			Mode:          0o644,
			SourceID:      filepath.Join("commands", cmd.Name+".md"),
			MergeStrategy: "replace",
		})
	}
	return ops, skips, nil
}
