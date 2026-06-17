package codex

import (
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMemory writes the canonical memory body verbatim to AGENTS.md.
func (a *Adapter) renderMemory(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	if c.Memory.Body == "" {
		return nil, nil
	}
	body := source.RenderManagedMemory(c.Memory.Body, c.Memory.Fragments, filepath.Base(p.Memory), c.Config.MemoryBannerEnabled())
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.Memory,
		Content:       []byte(body),
		Mode:          0o644,
		SourceID:      "memory/AGENTS.md",
		MergeStrategy: "replace",
	}}, nil
}
