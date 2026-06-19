package opencode

import (
	"fmt"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderSubagents translates Claude-shaped subagent frontmatter to
// OpenCode-shaped and emits FileOps for .config/opencode/agents/<name>.md.
//
// Frontmatter mapping:
//
//	description -> description  (direct copy)
//	model       -> model        (direct copy)
//	tools       -> drop + Skip  (OpenCode uses permission model; non-trivial mapping)
//	color       -> drop + Skip  (no OpenCode equivalent)
//	(none)      -> mode: subagent  (always added)
func (a *Adapter) renderSubagents(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, s := range c.Subagents {
		out := map[string]any{}
		if v, ok := s.Frontmatter["description"]; ok {
			out["description"] = v
		}
		if v, ok := s.Frontmatter["model"]; ok {
			out["model"] = v
		}
		out["mode"] = "subagent"

		// Drop unmappable fields, emit Skip notes so the user knows what was lost.
		if _, ok := s.Frontmatter["tools"]; ok {
			skips = append(skips, adapter.Skip{
				Component: "subagent", Name: s.Name,
				Reason: "Claude `tools` allowlist not projected to OpenCode `permission` (manual rule design needed)",
				Kind:   adapter.SkipReduced,
			})
		}
		if _, ok := s.Frontmatter["color"]; ok {
			skips = append(skips, adapter.Skip{
				Component: "subagent", Name: s.Name,
				Reason: "Claude `color` has no OpenCode equivalent",
				Kind:   adapter.SkipReduced,
			})
		}

		body, err := claude.EncodeFrontmatter(out, s.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("encode opencode subagent %s: %w", s.Name, err)
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
