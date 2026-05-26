package codex

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// codexAgentFile is the TOML shape of a Codex custom agent (~/.codex/agents/<name>.toml).
// Codex defines subagents as standalone TOML, where the prose lives under
// `developer_instructions` rather than a markdown body. agentsync projects the
// canonical markdown body onto developer_instructions and carries the
// `description` + `model` frontmatter; there is no per-agent tools allowlist.
type codexAgentFile struct {
	Name                  string `toml:"name"`
	Description           string `toml:"description,omitempty"`
	Model                 string `toml:"model,omitempty"`
	DeveloperInstructions string `toml:"developer_instructions"`
}

// codexAgentKnownKeys are the canonical frontmatter keys with a Codex TOML home.
// Anything else (tools, color, …) has no target and is dropped with a Skip.
var codexAgentKnownKeys = map[string]bool{"description": true, "model": true}

// renderSubagents projects canonical subagents into Codex agent TOML files.
// markdown body → developer_instructions; description + model carry over; any
// other frontmatter key (notably `tools` and `color`) has no Codex equivalent
// and is dropped with a reported Skip.
func (a *Adapter) renderSubagents(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, s := range c.Subagents {
		af := codexAgentFile{
			Name:                  s.Name,
			Description:           fmString(s.Frontmatter, "description"),
			Model:                 fmString(s.Frontmatter, "model"),
			DeveloperInstructions: s.Body,
		}
		if dropped := droppedKeys(s.Frontmatter, codexAgentKnownKeys); len(dropped) > 0 {
			skips = append(skips, adapter.Skip{
				Component: "subagent-frontmatter",
				Name:      s.Name,
				Reason: fmt.Sprintf("Codex agents are TOML with no per-agent tools allowlist; dropped %s",
					strings.Join(dropped, ", ")),
			})
		}
		body, err := toml.Marshal(af)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal subagent %s: %w", s.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.AgentsDir, s.Name+".toml"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("agents", s.Name+".md"),
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

// droppedKeys returns the sorted frontmatter keys NOT in known — the keys with
// no projection target, reported in a Skip so the loss is never silent.
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
