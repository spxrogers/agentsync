package cline

import (
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMemory projects the canonical memory body into a Cline rule at
// `.clinerules/agentsync.md`. Cline concatenates `.clinerules/` markdown files as
// instructions (plain — no frontmatter), so the body is written verbatim
// (byte-clean round-trip). Cline's GLOBAL rules live in `~/Documents/Cline/`
// (a non-XDG app path agentsync does not target), so at user scope
// (p.RulesDir == "") memory is reported as a skip.
func (a *Adapter) renderMemory(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if c.Memory.Body == "" {
		return nil, nil, nil
	}
	if p.RulesDir == "" {
		return nil, []adapter.Skip{{
			Component: "memory",
			Name:      "rules",
			Reason:    "Cline global rules live in ~/Documents/Cline/ (a non-XDG app path agentsync does not target); memory projects at project scope only (.clinerules/)",
		}}, nil
	}
	body := source.RenderManagedMemory(c.Memory.Body, c.Memory.Fragments, memoryRuleFile, c.Config.MemoryBannerEnabled())
	return []adapter.FileOp{{
		Action:        "write",
		Path:          filepath.Join(p.RulesDir, memoryRuleFile),
		Content:       []byte(body),
		Mode:          0o644,
		SourceID:      "memory/AGENTS.md",
		MergeStrategy: "replace",
	}}, nil, nil
}
