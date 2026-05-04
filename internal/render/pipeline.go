// Package render orchestrates the apply pipeline: canonical model + adapter
// registry -> per-agent FileOps + Skips. apply flag controls whether ops are
// written to disk or returned for inspection (e.g. --dry-run).
package render

import (
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// RenderPlan holds the result of rendering a canonical model through every
// selected adapter. PerAgent[name] is the per-agent breakdown.
type RenderPlan struct {
	PerAgent map[string]AgentResult
}

type AgentResult struct {
	Ops   []adapter.FileOp
	Skips []adapter.Skip
}

// Total returns the total number of FileOps across all agents.
func (p RenderPlan) Total() int {
	n := 0
	for _, r := range p.PerAgent {
		n += len(r.Ops)
	}
	return n
}

// Plan asks each adapter named in agents to render the canonical model.
// Returns a RenderPlan, never writes anything. Use Apply() to commit.
func Plan(c source.Canonical, reg *adapter.Registry, agents []string, scope adapter.Scope, project string) (RenderPlan, error) {
	out := RenderPlan{PerAgent: map[string]AgentResult{}}
	for _, name := range agents {
		a := reg.Lookup(name)
		if a == nil {
			return out, fmt.Errorf("adapter %q not registered", name)
		}
		ops, skips, err := a.Render(c, scope, project)
		if err != nil {
			return out, fmt.Errorf("render %s: %w", name, err)
		}
		out.PerAgent[name] = AgentResult{Ops: ops, Skips: skips}
	}
	return out, nil
}

// Apply commits a RenderPlan by calling each adapter's Apply with its FileOps.
// If any adapter returns an error, applies completed so far are NOT rolled back
// (each adapter's Apply is itself atomic per-file via iox.AtomicWrite).
func Apply(p RenderPlan, reg *adapter.Registry) error {
	for name, res := range p.PerAgent {
		a := reg.Lookup(name)
		if a == nil {
			return fmt.Errorf("adapter %q not registered at apply", name)
		}
		if err := a.Apply(res.Ops); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}
