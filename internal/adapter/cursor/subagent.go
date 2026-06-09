package cursor

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// cursorAgentKnownKeys are the subagent frontmatter keys Cursor recognizes in a
// `.cursor/agents/<name>.md` file (per cursor.com/docs/subagents): name,
// description, model ("inherit" or a model id), readonly, and is_background.
// Anything else — notably Claude's `tools` allowlist and `color` — has no Cursor
// equivalent and is dropped with a reported Skip.
var cursorAgentKnownKeys = map[string]bool{
	"name":          true,
	"description":   true,
	"model":         true,
	"readonly":      true,
	"is_background": true,
}

// renderSubagents projects canonical subagents into Cursor agent markdown files.
// The markdown body carries over verbatim; supported frontmatter keys carry
// over; any unsupported key (notably `tools` and `color`) is dropped with a
// reported Skip so the loss is never silent.
func (a *Adapter) renderSubagents(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, s := range c.Subagents {
		fm := map[string]any{}
		for k, v := range s.Frontmatter {
			if cursorAgentKnownKeys[k] {
				fm[k] = v
			}
		}
		if dropped := droppedKeys(s.Frontmatter, cursorAgentKnownKeys); len(dropped) > 0 {
			skips = append(skips, adapter.Skip{
				Component: "subagent-frontmatter",
				Name:      s.Name,
				Reason: fmt.Sprintf("Cursor subagents support only name/description/model/readonly/is_background; dropped %s",
					strings.Join(dropped, ", ")),
			})
		}
		body, err := claude.EncodeFrontmatter(fm, s.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("encode subagent %s: %w", s.Name, err)
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
	return ops, skips, nil
}

// droppedKeys returns the sorted frontmatter keys NOT in known, reported in a
// Skip so the loss is never silent.
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

// sortedKeys returns the keys of fm in sorted order.
func sortedKeys(fm map[string]any) []string {
	out := make([]string, 0, len(fm))
	for k := range fm {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
