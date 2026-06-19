package roo

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// rooCommandKnownKeys are the canonical command frontmatter keys Roo recognizes
// in a `.roo/commands/<name>.md` file: description and argument-hint (Roo keeps
// BOTH, unlike Cursor/Continue). Anything else (notably `allowed-tools`) has no
// Roo field and is dropped with a report. (Roo also supports a `mode` key, but
// that has no canonical source.)
var rooCommandKnownKeys = map[string]bool{"description": true, "argument-hint": true}

// renderCommands projects canonical slash commands into Roo command files
// (`.roo/commands/<name>.md`, invoked as `/<name>`). Roo commands are markdown
// with YAML frontmatter; `description` and `argument-hint` carry over, and any
// other key (e.g. `allowed-tools`) is dropped with a reported Skip. Both user and
// project scopes are supported.
func (a *Adapter) renderCommands(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, cmd := range c.Commands {
		fm := map[string]any{}
		for k, v := range cmd.Frontmatter {
			if rooCommandKnownKeys[k] {
				fm[k] = v
			}
		}
		if dropped := droppedKeys(cmd.Frontmatter, rooCommandKnownKeys); len(dropped) > 0 {
			skips = append(skips, adapter.Skip{
				Component: "command",
				Name:      cmd.Name,
				Reason:    "Roo commands support description + argument-hint; dropped " + strings.Join(dropped, ", "),
				Kind:      adapter.SkipReduced,
			})
		}
		body, err := claude.EncodeFrontmatter(fm, cmd.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("encode command %s: %w", cmd.Name, err)
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

// droppedKeys returns the sorted frontmatter keys NOT in known.
func droppedKeys(fm map[string]any, known map[string]bool) []string {
	var out []string
	for k := range fm {
		if !known[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
