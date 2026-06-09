package roo

import (
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMemory projects the canonical memory body into a Roo rule at
// `.roo/rules/agentsync.md`. Roo reads .roo/rules/ recursively as plain markdown
// instructions, so the body is written verbatim (byte-clean round-trip). Both
// user (~/.roo/rules) and project (<repo>/.roo/rules) scopes are supported.
func (a *Adapter) renderMemory(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	if c.Memory.Body == "" {
		return nil, nil
	}
	body := source.ExpandMemoryImports(c.Memory.Body, c.Memory.Fragments)
	return []adapter.FileOp{{
		Action:        "write",
		Path:          filepath.Join(p.RulesDir, memoryRuleFile),
		Content:       []byte(body),
		Mode:          0o644,
		SourceID:      "memory/AGENTS.md",
		MergeStrategy: "replace",
	}}, nil
}
