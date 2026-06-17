package cursor

import (
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMemory writes the canonical memory body verbatim to the repo-root
// AGENTS.md, which Cursor reads natively. At USER scope there is no filesystem
// target — Cursor keeps user-level rules in app-local storage — so the memory is
// reported as a Skip rather than written somewhere Cursor won't read it.
func (a *Adapter) renderMemory(c source.Canonical, p Paths, scope adapter.Scope) ([]adapter.FileOp, []adapter.Skip, error) {
	if c.Memory.Body == "" {
		return nil, nil, nil
	}
	if scope == adapter.ScopeUser {
		return nil, []adapter.Skip{{
			Component: "memory",
			Name:      "AGENTS.md",
			Reason:    "Cursor stores user-level rules in app-local storage; no filesystem projection target (use project scope for AGENTS.md)",
		}}, nil
	}
	body := source.RenderManagedMemory(c.Memory.Body, c.Memory.Fragments, filepath.Base(p.Memory), c.Config.MemoryBannerEnabled())
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.Memory,
		Content:       []byte(body),
		Mode:          0o644,
		SourceID:      "memory/AGENTS.md",
		MergeStrategy: "replace",
	}}, nil, nil
}
