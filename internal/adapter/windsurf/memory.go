package windsurf

import (
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMemory projects the canonical memory body into a Windsurf rule at
// `.windsurf/rules/agentsync.md`. Windsurf rules are plain markdown (activation
// is configured in the UI, not via frontmatter), so the body is written verbatim
// (byte-clean round-trip). Windsurf's GLOBAL rules are app-managed (not a
// filesystem path), so at user scope (p.RulesDir == "") memory is reported as a
// skip rather than written somewhere Windsurf won't read it.
func (a *Adapter) renderMemory(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if c.Memory.Body == "" {
		return nil, nil, nil
	}
	if p.RulesDir == "" {
		return nil, []adapter.Skip{{
			Component: "memory",
			Name:      "rules",
			Reason:    "Windsurf global rules are app-managed (not a filesystem path); memory projects at project scope only (.windsurf/rules/)",
		}}, nil
	}
	body := source.ExpandMemoryImports(c.Memory.Body, c.Memory.Fragments)
	return []adapter.FileOp{{
		Action:        "write",
		Path:          filepath.Join(p.RulesDir, memoryRuleFile),
		Content:       []byte(body),
		Mode:          0o644,
		SourceID:      "memory/AGENTS.md",
		MergeStrategy: "replace",
	}}, nil, nil
}
