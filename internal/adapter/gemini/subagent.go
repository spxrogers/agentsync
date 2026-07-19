package gemini

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// geminiAgentDroppedKeys are the ONLY subagent frontmatter keys agentsync refuses
// to project into a Gemini agent file (`.gemini/agents/<name>.md`, per
// geminicli.com/docs/core/subagents). Every OTHER captured frontmatter key —
// including Gemini's own native fields (`kind`, `temperature`, `max_turns`,
// `timeout_mins`, `mcpServers`) — is passed through verbatim, mirroring Claude's
// lossless subagent passthrough. A user's hand-authored native keys are captured
// into canonical by Ingest and must survive a re-render instead of being stripped
// by the whole-file replace. This is a drop-LIST, not an allowlist, on purpose:
// unknown/future Gemini-native keys survive by default rather than being clipped.
//
//   - `tools`: Gemini's tool vocabulary DIFFERS from Claude's (e.g.
//     `read_file`/`grep_search`, not `Read`/`Grep`), so copying a Claude `tools`
//     list verbatim would name tools Gemini doesn't have.
//   - `color`: not a documented Gemini agent field (a Claude-only presentation hint).
//
// Both are dropped with a reported Skip so the loss surfaces in the translation report.
var geminiAgentDroppedKeys = map[string]bool{
	"tools": true,
	"color": true,
}

// renderSubagents projects canonical subagents into Gemini agent markdown files.
// The markdown body becomes the agent's system prompt. Every captured frontmatter
// key is passed through verbatim EXCEPT the keys in geminiAgentDroppedKeys
// (`tools` — different tool vocabulary than Gemini; `color` — no Gemini field),
// which are dropped with a reported Skip; `name` is defaulted to the filename when
// absent (Gemini requires it). Native Gemini keys (kind/temperature/max_turns/
// timeout_mins/mcpServers/…) that round-tripped through canonical are preserved.
func (a *Adapter) renderSubagents(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, s := range c.Subagents {
		fm := map[string]any{}
		var dropped []string
		for k, v := range s.Frontmatter {
			if geminiAgentDroppedKeys[k] {
				dropped = append(dropped, k)
				continue
			}
			fm[k] = v
		}
		// Gemini requires a `name`; default it to the filename when the source
		// frontmatter omits it (Claude derives name from the filename).
		if _, ok := fm["name"]; !ok {
			fm["name"] = s.Name
		}
		// Report only the keys that were actually dropped — never the native keys
		// that now pass through. If nothing was dropped, emit no Skip.
		if len(dropped) > 0 {
			sort.Strings(dropped)
			skips = append(skips, adapter.Skip{
				Component: "subagent",
				Name:      s.Name,
				Reason: fmt.Sprintf("Gemini agents preserve native frontmatter (kind/temperature/max_turns/timeout_mins/mcpServers/…); Claude's `tools` vocabulary differs from Gemini's and `color` has no Gemini field, so dropped %s",
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
