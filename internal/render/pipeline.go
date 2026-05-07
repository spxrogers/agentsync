// Package render orchestrates the apply pipeline: canonical model + adapter
// registry -> per-agent FileOps + Skips. apply flag controls whether ops are
// written to disk or returned for inspection (e.g. --dry-run).
package render

import (
	"fmt"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
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
//
// s may be nil; when non-nil, OwnedKeys is populated on merge-json-keys ops
// from state.Keys so the apply pipeline knows which JSON-pointer paths it owns.
func Plan(c source.Canonical, reg *adapter.Registry, agents []string, scope adapter.Scope, project string, s *state.Targets) (RenderPlan, error) {
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
		if s != nil {
			for i, op := range ops {
				if op.MergeStrategy == "merge-json-keys" || op.MergeStrategy == "merge-jsonc-keys" {
					ops[i].OwnedKeys = ownedKeysFor(s, name, scope, project, op.Path)
				}
			}
		}
		out.PerAgent[name] = AgentResult{Ops: ops, Skips: skips}
	}
	return out, nil
}

// ownedKeysFor returns the JSON-pointer strings owned by agentsync for a given
// agent+scope+project+path combination, as recorded in state.Keys.
func ownedKeysFor(s *state.Targets, agent string, scope adapter.Scope, project, path string) []string {
	prefix := fmt.Sprintf("%s:%s:%s:%s:", agent, scope.String(), project, path)
	var out []string
	for k := range s.Keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	return out
}

// Apply commits a RenderPlan by calling each adapter's Apply with its FileOps.
// If any adapter returns an error, applies completed so far are NOT rolled back
// (each adapter's Apply is itself atomic per-file via iox.AtomicWrite).
//
// Deduplication: when two adapters emit a "write" op for the same path (e.g.
// a shared skill file written by both claude and opencode), the first one wins
// and the second is silently skipped. Content is deterministic per path, so
// skipping a duplicate is always safe.
func Apply(p RenderPlan, reg *adapter.Registry) error {
	seen := map[string]bool{}
	for name, res := range p.PerAgent {
		a := reg.Lookup(name)
		if a == nil {
			return fmt.Errorf("adapter %q not registered at apply", name)
		}
		var deduped []adapter.FileOp
		for _, op := range res.Ops {
			if op.Action == "write" {
				if seen[op.Path] {
					continue
				}
				seen[op.Path] = true
			}
			deduped = append(deduped, op)
		}
		if err := a.Apply(deduped); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}
