package cline

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderCommands projects canonical slash commands into Cline workflows
// (`.clinerules/workflows/<name>.md`, invoked as `/<name>`). Cline workflows are
// PLAIN markdown (steps as prose, no frontmatter), so only the body survives: a
// command's `description`/`argument-hint`/`allowed-tools` frontmatter is dropped
// with a reported Skip. Workflows are project-scoped; at user scope
// (p.WorkflowsDir == "") each command is skipped.
func (a *Adapter) renderCommands(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if p.WorkflowsDir == "" {
		var skips []adapter.Skip
		for _, cmd := range c.Commands {
			skips = append(skips, adapter.Skip{
				Component: "command",
				Name:      cmd.Name,
				Reason:    "Cline workflows are project-scoped (.clinerules/workflows/); no user-scope filesystem target",
			})
		}
		return nil, skips, nil
	}
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, cmd := range c.Commands {
		if len(cmd.Frontmatter) > 0 {
			skips = append(skips, adapter.Skip{
				Component: "command-frontmatter",
				Name:      cmd.Name,
				Reason:    "Cline workflows are plain markdown (no frontmatter); dropped " + strings.Join(sortedKeys(cmd.Frontmatter), ", "),
			})
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.WorkflowsDir, cmd.Name+".md"),
			Content:       []byte(cmd.Body),
			Mode:          0o644,
			SourceID:      filepath.Join("commands", cmd.Name+".md"),
			MergeStrategy: "replace",
		})
	}
	return ops, skips, nil
}

// sortedKeys returns the keys of fm in sorted order.
func sortedKeys(fm map[string]any) []string {
	out := make([]string, 0, len(fm))
	for k := range fm {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
