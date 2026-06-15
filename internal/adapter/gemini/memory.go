package gemini

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMemory writes the canonical memory body to GEMINI.md — `~/.gemini/GEMINI.md`
// at user scope, the repo-root `GEMINI.md` at project scope (the top of Gemini's
// hierarchical context load). Full fidelity: the same markdown, and agentsync's
// fragment imports are expanded the same way they are for every other agent.
func (a *Adapter) renderMemory(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	if c.Memory.Body == "" {
		return nil, nil
	}
	body := source.ExpandMemoryImports(c.Memory.Body, c.Memory.Fragments)
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.Memory,
		Content:       []byte(body),
		Mode:          0o644,
		SourceID:      "memory/AGENTS.md",
		MergeStrategy: "replace",
	}}, nil
}
