package continuedev

import (
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMemory projects the canonical memory body into a single Continue rule at
// `.continue/rules/agentsync.md`. A rule file with NO frontmatter is applied to
// every interaction (per docs.continue.dev/customize/deep-dives/rules) — i.e. it
// behaves as persistent memory/instructions — so the body is written verbatim
// (no frontmatter munging, byte-clean round-trip). Both user (~/.continue/rules)
// and project (<repo>/.continue/rules) scopes are supported.
func (a *Adapter) renderMemory(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	if c.Memory.Body == "" {
		return nil, nil
	}
	body := source.RenderManagedMemory(c.Memory.Body, c.Memory.Fragments, memoryRuleFile, c.Config.MemoryBannerEnabled())
	return []adapter.FileOp{{
		Action:        "write",
		Path:          filepath.Join(p.RulesDir, memoryRuleFile),
		Content:       []byte(body),
		Mode:          0o644,
		SourceID:      "memory/AGENTS.md",
		MergeStrategy: "replace",
	}}, nil
}
