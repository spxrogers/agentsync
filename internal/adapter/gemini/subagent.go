package gemini

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// geminiAgentKnownKeys are the subagent frontmatter keys agentsync projects to a
// Gemini agent file (`.gemini/agents/<name>.md`, per geminicli.com/docs/core/
// subagents): name, description, model. Gemini's `tools` list uses a DIFFERENT
// tool vocabulary than Claude (e.g. `read_file`/`grep_search`, not `Read`/`Grep`),
// so copying Claude's `tools` verbatim would reference tools Gemini doesn't have;
// it is dropped with a report instead (along with `color` and any other key).
// Gemini-only fields (kind, temperature, max_turns, timeout_mins, mcpServers) have
// no canonical source, so nothing is emitted for them.
var geminiAgentKnownKeys = map[string]bool{
	"name":        true,
	"description": true,
	"model":       true,
}

// renderSubagents projects canonical subagents into Gemini agent markdown files.
// The markdown body becomes the agent's system prompt; name (filename-derived if
// absent — Gemini requires it), description, and model carry over; any other
// frontmatter key (notably `tools` and `color`) is dropped with a reported Skip.
func (a *Adapter) renderSubagents(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, s := range c.Subagents {
		fm := map[string]any{}
		for k, v := range s.Frontmatter {
			if geminiAgentKnownKeys[k] {
				fm[k] = v
			}
		}
		// Gemini requires a `name`; default it to the filename when the source
		// frontmatter omits it (Claude derives name from the filename).
		if _, ok := fm["name"]; !ok {
			fm["name"] = s.Name
		}
		if dropped := droppedKeys(s.Frontmatter, geminiAgentKnownKeys); len(dropped) > 0 {
			skips = append(skips, adapter.Skip{
				Component: "subagent",
				Name:      s.Name,
				Reason: fmt.Sprintf("Gemini agents support name/description/model (its `tools` vocabulary differs from Claude's); dropped %s",
					strings.Join(dropped, ", ")),
				Kind: adapter.SkipReduced,
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
