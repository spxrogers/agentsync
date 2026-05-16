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

// PreviewCollisions runs the same per-file / per-pointer foreign-collision
// check that Apply runs at write time, but writes nothing. It returns the
// reports a real apply would have produced. Used by `apply --dry-run` so
// the user can see — before committing — exactly which destination files
// are about to be backed up and overwritten.
//
// We have to re-run the deduplication that Apply does (path-level
// first-writer-wins) so the dry-run preview matches what really happens.
func PreviewCollisions(
	p RenderPlan,
	reg *adapter.Registry,
	st *state.Targets,
	home string,
	scope adapter.Scope,
	project string,
) []CollisionReport {
	if st == nil {
		return nil
	}
	var all []CollisionReport
	seen := map[string]bool{}
	for _, name := range reg.Names() {
		res, ok := p.PerAgent[name]
		if !ok {
			continue
		}
		w := NewPreviewWriter(st, home, scope, project, name)
		for _, op := range res.Ops {
			if op.Action != "" && op.Action != "write" {
				continue
			}
			if seen[op.Path] {
				continue
			}
			seen[op.Path] = true
			// We don't have the post-merge finalBytes here without
			// re-running the adapter. For preview purposes a conservative
			// "is something there that doesn't look exactly like our op"
			// is sufficient — Apply's real maybeBackup will recompute and
			// suppress the report if the on-disk content happens to match
			// the post-merge bytes exactly.
			_ = w.maybeBackup(op, op.Content)
		}
		all = append(all, w.Reports()...)
	}
	return all
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

// Apply commits a RenderPlan by constructing one Writer per agent and
// invoking adapter.Apply with that writer. The writer enforces the
// foreign-collision backup invariant on every destination write — there
// is no separate "guard" pass; the guarantee is intrinsic to the only
// write path adapters are permitted to use.
//
// Returns the union of CollisionReports across all agents so the caller
// can surface them. If any adapter returns an error, applies completed
// so far are NOT rolled back (each underlying iox.AtomicWrite is atomic
// per-file, but the plan as a whole is not transactional).
//
// Deduplication: when two adapters emit a "write" op for the same path
// (e.g. a shared skill file written by both claude and opencode), the
// first one wins and the second is silently skipped. Content is
// deterministic per path, so skipping a duplicate is always safe.
func Apply(
	p RenderPlan,
	reg *adapter.Registry,
	st *state.Targets,
	home string,
	scope adapter.Scope,
	project string,
) ([]CollisionReport, error) {
	if st == nil {
		// Defensive: a nil state would make every write look like a
		// foreign collision and produce duplicate backups. Callers that
		// don't yet have a state object should construct an empty one
		// (state.New) rather than passing nil.
		return nil, fmt.Errorf("render.Apply: nil state")
	}

	var allReports []CollisionReport
	seen := map[string]bool{}
	for _, name := range reg.Names() {
		res, ok := p.PerAgent[name]
		if !ok {
			continue
		}
		a := reg.Lookup(name)
		if a == nil {
			return allReports, fmt.Errorf("adapter %q not registered at apply", name)
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
		w := NewWriter(st, home, scope, project, name)
		if err := a.Apply(deduped, w); err != nil {
			allReports = append(allReports, w.Reports()...)
			return allReports, fmt.Errorf("apply %s: %w", name, err)
		}
		allReports = append(allReports, w.Reports()...)
	}
	return allReports, nil
}
