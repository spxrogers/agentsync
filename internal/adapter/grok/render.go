package grok

import (
	"fmt"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func (a *Adapter) Render(r secrets.Resolved, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, nil, err
	}
	if err := a.validateHome(); err != nil {
		return nil, nil, err
	}
	c := r.Canonical() //nolint:forbidigo // sanctioned render egress: resolved model goes only to native FileOps
	if scope == adapter.ScopeProject && c.Project != nil {
		c = *c.Project
	}
	p := a.resolvePaths(scope, project)
	ops, skips, err := renderMCP(c, p)
	if err != nil {
		return nil, nil, err
	}
	if c.Memory.Body != "" || len(c.Memory.Fragments) > 0 {
		body := source.RenderManagedMemory(c.Memory.Body, c.Memory.Fragments, filepath.Base(p.Memory), c.Config.MemoryBannerEnabled())
		ops = append(ops, adapter.FileOp{
			Path: p.Memory, Content: []byte(body), Mode: 0o644,
			SourceID: "memory/AGENTS.md", MergeStrategy: "replace",
		})
	}
	skillOps, err := claude.SkillFileOps(c.Skills, p.SkillsDir)
	if err != nil {
		return nil, nil, err
	}
	ops = append(ops, skillOps...)
	for _, cmd := range c.Commands {
		body, err := claude.EncodeFrontmatter(cmd.Frontmatter, cmd.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("encode command %s: %w", cmd.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Path:    filepath.Join(p.CommandsDir, cmd.Name+".md"),
			Content: body, Mode: 0o644, SourceID: filepath.Join("commands", cmd.Name+".md"), MergeStrategy: "replace",
		})
		if _, present := cmd.Frontmatter["allowed-tools"]; present {
			skips = append(skips, adapter.Skip{
				Component: "command", Name: cmd.Name, Kind: adapter.SkipReduced,
				Reason: "Grok preserves allowed-tools as metadata; it does not restrict or grant tools",
			})
		}
	}
	hookOps, hookSkips, err := renderHooks(c.Hooks, p)
	if err != nil {
		return nil, nil, err
	}
	ops = append(ops, hookOps...)
	skips = append(skips, hookSkips...)
	for _, s := range c.Subagents {
		skips = append(skips, adapter.Skip{
			Component: "subagent", Name: s.Name, Kind: adapter.SkipDropped,
			Reason: "Grok subagent definitions are not projected by this adapter",
		})
	}
	for _, l := range c.LSPServers {
		skips = append(skips, adapter.Skip{
			Component: "lsp", Name: l.ID, Kind: adapter.SkipDropped,
			Reason: "Grok LSP configuration is not projected by this adapter",
		})
	}
	return ops, skips, nil
}
