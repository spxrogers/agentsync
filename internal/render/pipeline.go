// Package render orchestrates the apply pipeline: canonical model + adapter
// registry -> per-agent FileOps + Skips. apply flag controls whether ops are
// written to disk or returned for inspection (e.g. --dry-run).
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/secrets"
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

// isKeyMerge reports whether a MergeStrategy accumulates JSON pointers into a
// shared file (rather than replacing the whole file). Such ops must never be
// deduped by path — one agent emits several of them to the same destination.
func isKeyMerge(strategy string) bool {
	return strategy == "merge-json-keys" || strategy == "merge-jsonc-keys"
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
//
// userHome (the user's $HOME, paths.HomeDir) is required so OwnedKeys
// lookups match the HOME-relative form state stores. It is NOT the agentsync
// home — dest files live under $HOME, not under ~/.agentsync.
func Plan(r secrets.Resolved, reg *adapter.Registry, agents []string, scope adapter.Scope, project string, s *state.Targets, userHome string) (RenderPlan, error) {
	out := RenderPlan{PerAgent: map[string]AgentResult{}}
	for _, name := range agents {
		a := reg.Lookup(name)
		if a == nil {
			return out, fmt.Errorf("adapter %q not registered", name)
		}
		ops, skips, err := a.Render(r, scope, project)
		if err != nil {
			return out, fmt.Errorf("render %s: %w", name, err)
		}
		if s != nil {
			for i, op := range ops {
				if isKeyMerge(op.MergeStrategy) {
					owned := ownedKeysFor(s, name, scope, project, op.Path, userHome)
					// Scope each op's OwnedKeys to the top-level sections THIS op
					// writes. Several ops can target one file (claude writes
					// mcpServers, hooks, AND lspServers to settings.json); without
					// scoping, every op carried the union of owned pointers and
					// MergeKeys' removal step deleted the OTHER ops' sections —
					// last op wins, earlier sections wiped on the next apply.
					ops[i].OwnedKeys = scopeOwnedToSections(owned, op.Content)
				}
			}
			// Cleanup synthesis: when a key-merge section becomes empty in the
			// source (e.g. the user removes their last MCP server), the
			// adapter renders NO op for that file, so the owned key would
			// linger in the destination forever. Synthesize an empty merge op
			// per orphaned dest path so MergeKeys deletes the dead keys.
			ops = append(ops, orphanCleanupOps(s, a, name, scope, project, userHome, ops)...)
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
	userHome string,
	scope adapter.Scope,
	project string,
) ([]CollisionReport, error) {
	if st == nil {
		return nil, nil
	}
	var all []CollisionReport
	seen := map[string][]byte{}
	for _, name := range reg.Names() {
		res, ok := p.PerAgent[name]
		if !ok {
			continue
		}
		w := NewPreviewWriter(st, home, userHome, scope, project, name)
		for _, op := range res.Ops {
			if op.Action != "" && op.Action != "write" {
				continue
			}
			// Mirror Apply's dedup AND its divergence guard: only whole-file
			// replace writes are collapsed by path; identical content dedups,
			// but divergent content for the same path fails loud — otherwise
			// --dry-run would show a clean preview that the real apply rejects.
			if !isKeyMerge(op.MergeStrategy) {
				if prev, ok := seen[op.Path]; ok {
					if !bytes.Equal(prev, op.Content) {
						return all, fmt.Errorf(
							"agent %q renders different content than an earlier agent for the same path %s; "+
								"refusing to silently drop one (shared paths must render identical bytes)",
							name, op.Path,
						)
					}
					continue
				}
				seen[op.Path] = op.Content
			}
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
	return all, nil
}

// ownedKeysFor returns the JSON-pointer strings owned by agentsync for a given
// agent+scope+project+path combination, as recorded in state.Keys.
func ownedKeysFor(s *state.Targets, agent string, scope adapter.Scope, project, path, userHome string) []string {
	prefix := fmt.Sprintf("%s:%s:%s:%s:", agent, scope.String(),
		paths.HomeRelative(userHome, project), paths.HomeRelative(userHome, path))
	var out []string
	for k := range s.Keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		ptr := strings.TrimPrefix(k, prefix)
		// The remainder must be a JSON pointer, which always begins with '/'.
		// If it doesn't, this key belongs to a DIFFERENT dest path that merely
		// shares a colon-delimited string prefix with `path` (e.g. dest "a" vs
		// "a:b", realistic only for a Windows drive-letter path stored
		// absolute). Claiming it would inject a foreign pointer into this op's
		// OwnedKeys and corrupt ownership — the same colon-ambiguity class
		// PruneStaleState and orphanCleanupOps already guard against.
		if !strings.HasPrefix(ptr, "/") {
			continue
		}
		out = append(out, ptr)
	}
	return out
}

// scopeOwnedToSections filters owned JSON pointers down to those whose
// top-level section is present in content (an op's rendered `ours`). Several
// key-merge ops can target one file; each must only carry — and therefore only
// be able to delete — pointers under the section(s) it actually writes. If
// content can't be parsed we return none rather than all, so a malformed op
// can never drive a cross-section deletion.
func scopeOwnedToSections(owned []string, content []byte) []string {
	if len(owned) == 0 {
		return owned
	}
	var ours map[string]any
	if err := json.Unmarshal(content, &ours); err != nil || ours == nil {
		return nil
	}
	sections := make(map[string]struct{}, len(ours))
	for k := range ours {
		sections[escapeJSONPointer(k)] = struct{}{}
	}
	var out []string
	for _, p := range owned {
		if _, ok := sections[firstPointerSegment(p)]; ok {
			out = append(out, p)
		}
	}
	return out
}

// firstPointerSegment returns the first (escaped) path segment of a JSON
// pointer, i.e. its top-level section: "/mcpServers/github" → "mcpServers".
func firstPointerSegment(ptr string) string {
	ptr = strings.TrimPrefix(ptr, "/")
	if i := strings.IndexByte(ptr, '/'); i >= 0 {
		return ptr[:i]
	}
	return ptr
}

// orphanCleanupOps synthesizes empty key-merge ops that remove keys the agent
// owns in state but no longer renders this run — the case where a source
// section went fully empty (e.g. the user deleted their last MCP server), so
// no real merge op exists to carry the removal via OwnedKeys. Without this the
// dead key lingers in the destination forever.
//
// Safety (validated): the strategy is the adapter's exact, static
// KeyMergeStrategy() — never inferred — so a JSONC opencode.json is never
// merged with the strict-JSON path (which would clobber it). A dest that no
// longer exists on disk is skipped (no empty "{}" file is created); the stale
// state entry is dropped by PruneStaleState instead.
func orphanCleanupOps(s *state.Targets, a adapter.Adapter, agent string, scope adapter.Scope, project, userHome string, rendered []adapter.FileOp) []adapter.FileOp {
	strat := a.KeyMergeStrategy()
	if strat == "" {
		return nil // adapter doesn't merge keys
	}
	// Portable dest path → set of escaped top-level sections the agent rendered
	// a key-merge op for this run. A section that still has a real op handles
	// its own per-key removals via that op's (section-scoped) OwnedKeys; only a
	// section with NO op this run is orphan-cleaned here. Tracking per-section
	// (not per-path) is required now that OwnedKeys are section-scoped: removing
	// a whole section leaves no op carrying its pointers, so the remaining
	// sections' ops can no longer delete it.
	renderedSections := map[string]map[string]struct{}{}
	for _, op := range rendered {
		if !isKeyMerge(op.MergeStrategy) {
			continue
		}
		pp := paths.HomeRelative(userHome, op.Path)
		secs := renderedSections[pp]
		if secs == nil {
			secs = map[string]struct{}{}
			renderedSections[pp] = secs
		}
		var ours map[string]any
		if json.Unmarshal(op.Content, &ours) == nil {
			for k := range ours {
				secs[escapeJSONPointer(k)] = struct{}{}
			}
		}
	}

	// Group owned pointers by their portable dest path from state.Keys.
	prefix := fmt.Sprintf("%s:%s:%s:", agent, scope.String(), paths.HomeRelative(userHome, project))
	ownedByPath := map[string][]string{}
	var order []string
	for k := range s.Keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		// rest = "<portablePath>:<pointer>". The pointer is a JSON pointer
		// that always starts with '/', so the ":/" sequence marks the
		// boundary. This is reliable for the realistic path shapes: portable
		// "${HOME}/..." paths and POSIX absolute paths contain no ':', and
		// Windows absolute paths use '\' (so a drive is "C:\", not "C:/").
		// In the pathological case where a path or pointer-key still contains
		// a literal ":/", a mis-split yields a path that fails the os.Stat
		// existence check below and is harmlessly skipped (the orphan simply
		// isn't cleaned that run) — it never deletes the wrong data.
		i := strings.Index(rest, ":/")
		if i < 0 {
			continue
		}
		path, ptr := rest[:i], rest[i+1:]
		if secs, ok := renderedSections[path]; ok {
			if _, sectionRendered := secs[firstPointerSegment(ptr)]; sectionRendered {
				continue // a real op for this section handles its own removals
			}
		}
		if _, seen := ownedByPath[path]; !seen {
			order = append(order, path)
		}
		ownedByPath[path] = append(ownedByPath[path], ptr)
	}

	var cleanup []adapter.FileOp
	for _, path := range order {
		abs := paths.FromHomeRelative(userHome, path)
		// If the dest was already deleted, there's nothing to clean — skip so
		// we don't create an empty "{}" file. PruneStaleState drops the entry.
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		cleanup = append(cleanup, adapter.FileOp{
			Action:        "write",
			Path:          abs,
			Content:       []byte("{}"),
			Mode:          0o644,
			MergeStrategy: strat,
			OwnedKeys:     ownedByPath[path],
		})
	}
	return cleanup
}

// Apply commits a RenderPlan by constructing one Writer per agent and
// invoking adapter.Apply with that writer. The writer enforces the
// foreign-collision backup invariant on every destination write — there
// is no separate "guard" pass; the guarantee is intrinsic to the only
// write path adapters are permitted to use.
//
// Returns the union of CollisionReports across all agents so the caller
// can surface them, plus the set of destination paths actually written this
// run (so the apply-error rescue can record state for exactly those files and
// no others). If any adapter returns an error, applies completed so far are
// NOT rolled back (each underlying iox.AtomicWrite is atomic per-file, but the
// plan as a whole is not transactional); the reports and written-set returned
// reflect the work done before the failure.
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
	userHome string,
	scope adapter.Scope,
	project string,
) ([]CollisionReport, map[string]bool, error) {
	written := map[string]bool{}
	if st == nil {
		// Defensive: a nil state would make every write look like a
		// foreign collision and produce duplicate backups. Callers that
		// don't yet have a state object should construct an empty one
		// (state.New) rather than passing nil.
		return nil, written, fmt.Errorf("render.Apply: nil state")
	}

	var allReports []CollisionReport
	seen := map[string][]byte{}
	for _, name := range reg.Names() {
		res, ok := p.PerAgent[name]
		if !ok {
			continue
		}
		a := reg.Lookup(name)
		if a == nil {
			return allReports, written, fmt.Errorf("adapter %q not registered at apply", name)
		}
		var deduped []adapter.FileOp
		for _, op := range res.Ops {
			// Only whole-file replace writes are deduped (e.g. a shared
			// SKILL.md written identically by claude and opencode). Key-merge
			// ops are NOT deduped: a single agent emits separate
			// merge-json-keys ops to one file (claude writes mcpServers,
			// hooks, AND lspServers to settings.json), and each must run —
			// the adapter re-reads and merges per op. Deduping them by path
			// silently dropped every merge op after the first.
			if op.Action == "write" && !isKeyMerge(op.MergeStrategy) {
				if prev, ok := seen[op.Path]; ok {
					// Identical content is the safe, expected dedup (claude and
					// opencode render byte-identical SKILL.md). Divergent content
					// for the same path is a projection bug, and silently
					// dropping one agent's bytes would be data loss — so fail
					// loud rather than pick a winner.
					if !bytes.Equal(prev, op.Content) {
						return allReports, written, fmt.Errorf(
							"agent %q renders different content than an earlier agent for the same path %s; "+
								"refusing to silently drop one (shared paths must render identical bytes)",
							name, op.Path,
						)
					}
					continue
				}
				seen[op.Path] = op.Content
			}
			deduped = append(deduped, op)
		}
		w := NewWriter(st, home, userHome, scope, project, name)
		err := a.Apply(deduped, w)
		allReports = append(allReports, w.Reports()...)
		for path := range w.Wrote() {
			written[path] = true
		}
		if err != nil {
			return allReports, written, fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return allReports, written, nil
}
