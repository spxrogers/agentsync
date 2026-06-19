package continuedev

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// continueCommandKnownKeys are the canonical command frontmatter keys with a
// Continue prompt-block home. Anything else (argument-hint, allowed-tools, …) is
// dropped + reported.
var continueCommandKnownKeys = map[string]bool{"description": true}

// renderCommands projects canonical slash commands into Continue prompt blocks
// (`.continue/prompts/<name>.md`). A prompt block is markdown with frontmatter
// `name` + optional `description` + `invokable: true` (so it shows as the slash
// command `/<name>`) and the body as the prompt. `argument-hint`/`allowed-tools`
// have no Continue field and are dropped with a reported Skip.
func (a *Adapter) renderCommands(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, cmd := range c.Commands {
		fm := map[string]any{"name": cmd.Name, "invokable": true}
		if d := fmString(cmd.Frontmatter, "description"); d != "" {
			fm["description"] = d
		}
		if dropped := droppedKeys(cmd.Frontmatter, continueCommandKnownKeys); len(dropped) > 0 {
			skips = append(skips, adapter.Skip{
				Component: "command",
				Name:      cmd.Name,
				Reason:    "Continue prompt blocks carry only name/description; dropped " + strings.Join(dropped, ", "),
				Kind:      adapter.SkipReduced,
			})
		}
		body, err := claude.EncodeFrontmatter(fm, cmd.Body)
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
	return ops, skips, nil
}

// fmString returns the string value at key in a frontmatter map, or "".
func fmString(fm map[string]any, key string) string {
	s, _ := fm[key].(string)
	return s
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
