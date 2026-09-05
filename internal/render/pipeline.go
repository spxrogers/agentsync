// Package render orchestrates the apply pipeline: canonical model + adapter
// registry -> per-agent FileOps + Skips. apply flag controls whether ops are
// written to disk or returned for inspection (e.g. --dry-run).
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/secrets"
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

// IsKeyMerge reports whether a MergeStrategy accumulates pointers into a shared
// file (rather than replacing the whole file). Such ops must never be deduped by
// path — one agent emits several of them to the same destination. The rendered
// op.Content is always JSON regardless of the on-disk format (TOML for
// merge-toml-keys); only the destination file is decoded per strategy via
// decodeDestObject.
func IsKeyMerge(strategy string) bool {
	return strategy == "merge-json-keys" ||
		strategy == "merge-jsonc-keys" ||
		strategy == "merge-toml-keys"
}

// decodeDestObject parses a destination config file into a generic map per the
// op's merge strategy: TOML for merge-toml-keys (Codex config.toml), JSON
// otherwise (.claude.json, settings.json, the standardized opencode.json,
// hooks.json). The pointer-merge currency is always map[string]any, so callers
// — the foreign-collision backup and post-apply state recording — treat both
// on-disk formats uniformly. Empty input yields an empty map.
func decodeDestObject(strategy string, data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	unmarshal := json.Unmarshal
	if strategy == "merge-toml-keys" {
		unmarshal = toml.Unmarshal
	}
	m := map[string]any{}
	if err := unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil { // e.g. a literal "null" document
		m = map[string]any{}
	}
	return m, nil
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
	// Primary path-traversal guard (symmetric with capture.Capture): every
	// component id an adapter joins into a destination filename — subagent, command,
	// skill Names AND MCP/LSP server ids (continuedev writes one file per MCP
	// server) — must be a clean single-segment id before that join. A Name/id like
	// "../../../tmp/x" would otherwise render a FileOp.Path that escapes the agent's
	// config dir (a write-anywhere primitive on apply); a marketplace-projected MCP
	// id is an especially untrusted source (a raw manifest key). The dest->source
	// boundary already refuses such an id (source.ValidateComponentID); enforce the
	// SAME sanitizer here at the single dispatch waist, so both boundaries share one
	// source of truth (it also rejects control/deceptive runes via
	// untrusted.Sanitize, not just ".."). The id set is model-wide (agent-agnostic),
	// so validate it ONCE up front, not per-agent; a traversal-bearing id refuses
	// the whole plan (never an adapter.Skip). Names only — ComponentIDs is a
	// string-only accessor, so this never unwraps the Resolved.
	for _, id := range r.ComponentIDs() {
		if err := source.ValidateComponentID(id.Kind, id.Name); err != nil {
			return out, fmt.Errorf("render: component %s %q: %w", id.Kind, id.Name, err)
		}
	}
	for _, name := range agents {
		a := reg.Lookup(name)
		if a == nil {
			return out, fmt.Errorf("adapter %q not registered", name)
		}
		// Narrow the model to what this agent should receive before the adapter
		// sees it: a plugin whose `agents` allowlist excludes this agent
		// contributes nothing here. Enforced once, at the waist — see
		// secrets.Resolved.ForAgent and source.PluginTargetsAgent for why no
		// adapter participates. Components dropped this way leave no op, so a
		// destination file written by an earlier apply is reclaimed by the normal
		// orphan paths (orphanDeletes for file-backed kinds, orphanCleanupOps for
		// the key-merge ones) rather than lingering as the duplicate the
		// allowlist was set to remove.
		ops, skips, err := a.Render(r.ForAgent(name), scope, project)
		if err != nil {
			return out, fmt.Errorf("render %s: %w", name, err)
		}
		// Containment backstop (defense-in-depth): reject any write whose own cleaned
		// path still contains a ".." segment (an unrooted / relative-with-leading-".."
		// path). This is deliberately narrow — Plan does not know each adapter's dest
		// root, and filepath.Join collapses interior ".." into a rooted ABSOLUTE path,
		// so a traversal that resolves to an absolute path is NOT caught here; that
		// case is prevented upstream by the component-id validation above (which now
		// covers MCP/LSP ids too). Bundled skill-file paths (Skill.Files[*].Path)
		// legitimately contain '/', so they bypass ValidateComponentID, but they are
		// derived from a filesystem walk via filepath.Rel with symlinks skipped, so a
		// ".." can never enter them — this remains the last conservative net.
		for _, op := range ops {
			if op.Action == adapter.ActionWrite && pathEscapes(op.Path) {
				return out, fmt.Errorf("render %s: refusing FileOp path %q: escapes its destination directory via '..'", name, op.Path)
			}
		}
		if s != nil {
			for i, op := range ops {
				if IsKeyMerge(op.MergeStrategy) {
					owned := ownedKeysFor(s, name, scope, project, op.Path, userHome)
					// Scope each op's OwnedKeys to the top-level sections THIS op
					// writes. Several ops can target one file (codex writes
					// mcp_servers AND hooks to config.toml); without scoping, every
					// op carried the union of owned pointers and MergeKeys' removal
					// step deleted the OTHER ops' sections — last op wins, earlier
					// sections wiped on the next apply.
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

// pathEscapes reports whether a cleaned destination path still contains a ".."
// segment — i.e. it would traverse out of wherever the adapter rooted it. It is
// the conservative containment backstop behind Plan's primary component-id check:
// because Plan does not know each adapter's dest root, it can only reject a path
// whose own cleaned form escapes upward, not verify positive containment.
// filepath.Clean collapses interior traversal on a rooted absolute path, so a
// surviving ".." means the path is not anchored (relative-with-leading-.. or
// otherwise unrooted) — refuse it rather than write anywhere.
func pathEscapes(p string) bool {
	for _, seg := range strings.Split(filepath.Clean(p), string(filepath.Separator)) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// PreviewApply runs the apply pipeline through non-writing preview writers and
// reports what a real apply would do, without touching disk. It returns the
// foreign-collision reports a real apply would produce, plus the per-destination
// convergence verdict: `unchanged` paths already hold exactly the bytes apply
// would write (the dry-run shows them as "synced"), and `wouldChange` paths
// would be created or modified (shown as "write"). Used by `apply --dry-run`.
//
// It supersedes the older op.Content-only collision preview: because it runs the
// real adapter Apply, each merge is performed for real (against the on-disk
// destination), so the preview's synced/write verdict and collision set match
// the eventual apply exactly rather than approximating it.
func PreviewApply(
	p RenderPlan,
	reg *adapter.Registry,
	st *state.Targets,
	home string,
	userHome string,
	scope adapter.Scope,
	project string,
) (reports []CollisionReport, unchanged, wouldChange map[string]bool, err error) {
	reports, _, unchanged, wouldChange, err = applyPlan(p, reg, st, home, userHome, scope, project, true)
	return reports, unchanged, wouldChange, err
}

// ownedKeysFor returns the JSON-pointer strings owned by agentsync for a given
// agent+scope+project+path combination, as recorded in state.Keys.
func ownedKeysFor(s *state.Targets, agent string, scope adapter.Scope, project, path, userHome string) []string {
	portableProject := paths.HomeRelative(userHome, project)
	portablePath := paths.HomeRelative(userHome, path)
	var out []string
	for k := range s.Keys {
		if !k.InTree(agent, scope.String(), portableProject) || k.Path != portablePath {
			continue
		}
		out = append(out, k.Pointer)
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
		if !IsKeyMerge(op.MergeStrategy) {
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
	scopeName := scope.String()
	portableProject := paths.HomeRelative(userHome, project)
	ownedByPath := map[string][]string{}
	var order []string
	for k := range s.Keys {
		if !k.InTree(agent, scopeName, portableProject) {
			continue
		}
		if secs, ok := renderedSections[k.Path]; ok {
			if _, sectionRendered := secs[firstPointerSegment(k.Pointer)]; sectionRendered {
				continue // a real op for this section handles its own removals
			}
		}
		if _, seen := ownedByPath[k.Path]; !seen {
			order = append(order, k.Path)
		}
		ownedByPath[k.Path] = append(ownedByPath[k.Path], k.Pointer)
	}

	var cleanup []adapter.FileOp
	for _, path := range order {
		abs := paths.FromHomeRelative(userHome, path)
		// If the dest was already deleted, there's nothing to clean — skip so
		// we don't create an empty "{}" file. PruneStaleState drops the entry.
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		cleanup = append(cleanup, adapter.NewCleanupOp(abs, strat, ownedByPath[path]))
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
) ([]CollisionReport, map[string]bool, map[string]bool, error) {
	reports, written, unchanged, _, err := applyPlan(p, reg, st, home, userHome, scope, project, false)
	return reports, written, unchanged, err
}

// applyPlan is the shared body of Apply and PreviewApply. With dryRun=false it
// commits the plan through real Writers (the public Apply); with dryRun=true it
// runs the identical pipeline — same dedup, same skill-orphan reclamation, same
// per-op merge — through non-writing preview Writers, so the collision reports
// and the synced/would-change verdicts a dry-run shows are computed by the very
// code a real apply runs and can never disagree with it.
//
// It returns the union of CollisionReports plus three path sets: `written`
// (committed this run; empty under dryRun), `unchanged` (the destination already
// held our exact bytes), and `wouldChange` (a dry-run write that would create or
// modify the destination; empty on a real apply, since the write is performed).
// On adapter error the work done before the failure is reflected and not rolled
// back (per-file writes are atomic, the plan as a whole is not transactional).
func applyPlan(
	p RenderPlan,
	reg *adapter.Registry,
	st *state.Targets,
	home string,
	userHome string,
	scope adapter.Scope,
	project string,
	dryRun bool,
) (reports []CollisionReport, written, unchanged, wouldChange map[string]bool, err error) {
	written = map[string]bool{}
	unchanged = map[string]bool{}
	wouldChange = map[string]bool{}
	if st == nil {
		// Defensive: a nil state would make every write look like a
		// foreign collision and produce duplicate backups. Callers that
		// don't yet have a state object should construct an empty one
		// (state.New) rather than passing nil.
		return nil, written, unchanged, wouldChange, fmt.Errorf("render.Apply: nil state")
	}

	// seen is NOT reset per agent — that is deliberate, since the guard below
	// exists to catch two agents rendering one shared path differently. But it
	// means the guard also fires when a SINGLE agent emits two divergent ops for
	// one path (two canonical components resolving to the same destination), so
	// seenBy records which agent produced each path and the message distinguishes
	// the two cases. Before #211 an intra-agent collision was reported as
	// "different content than an earlier agent", sending the user looking for a
	// second agent that did not exist.
	seen := map[string][]byte{}
	seenBy := map[string]string{}
	deletedOrphans := map[string]struct{}{}
	for _, name := range reg.Names() {
		res, ok := p.PerAgent[name]
		if !ok {
			continue
		}
		a := reg.Lookup(name)
		if a == nil {
			return reports, written, unchanged, wouldChange, fmt.Errorf("adapter %q not registered at apply", name)
		}
		// Containment backstop for caller-built plans. Apply and PreviewApply
		// are exported and accept a RenderPlan that never went through Plan, so
		// the traversal check runs again here: a hand-built plan cannot smuggle
		// an unanchored ".." path past the guard Plan enforces. (A Plan-built
		// plan already passed it, so this is a no-op for it.) Scope honesty:
		// this covers the Apply/PreviewApply funnel only — cli/reconcile.go and
		// cli/agent.go call an adapter's Apply directly with plan-derived or
		// state-derived ops and never pass through here.
		for i := range res.Ops {
			if res.Ops[i].Action == adapter.ActionWrite && pathEscapes(res.Ops[i].Path) {
				return reports, written, unchanged, wouldChange, fmt.Errorf(
					"apply %s: refusing FileOp path %q: escapes its destination directory via '..'", name, res.Ops[i].Path,
				)
			}
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
			if op.Action == adapter.ActionWrite && !IsKeyMerge(op.MergeStrategy) {
				if prev, ok := seen[op.Path]; ok {
					// Identical content is the safe, expected dedup (claude and
					// opencode render byte-identical SKILL.md). Divergent content
					// for the same path is a projection bug, and silently
					// dropping one agent's bytes would be data loss — so fail
					// loud rather than pick a winner.
					if !bytes.Equal(prev, op.Content) {
						if seenBy[op.Path] == name {
							return reports, written, unchanged, wouldChange, fmt.Errorf(
								"agent %q renders two different contents for the same path %s; "+
									"refusing to silently drop one — two canonical components resolve "+
									"to one destination file, so rename one of them",
								name, op.Path,
							)
						}
						return reports, written, unchanged, wouldChange, fmt.Errorf(
							"agent %q renders different content than agent %q for the same path %s; "+
								"refusing to silently drop one (shared paths must render identical bytes)",
							name, seenBy[op.Path], op.Path,
						)
					}
					continue
				}
				seen[op.Path] = op.Content
				seenBy[op.Path] = name
			}
			deduped = append(deduped, op)
		}
		// Reclaim skill files (a whole skill, or a single bundled file) that
		// this agent owns in state but the source no longer renders. Deduped by
		// path across agents that share a skills dir (claude + opencode →
		// .claude/skills/), since the first agent's writer already removed it.
		for _, del := range orphanDeletes(st, userHome, name, scope, project, res.Ops) {
			if _, done := deletedOrphans[del.Path]; done {
				continue
			}
			deletedOrphans[del.Path] = struct{}{}
			deduped = append(deduped, del)
		}
		var w *Writer
		if dryRun {
			w = NewPreviewWriter(st, home, userHome, scope, project, name)
		} else {
			w = NewWriter(st, home, userHome, scope, project, name)
		}
		aerr := a.Apply(deduped, w)
		reports = append(reports, w.Reports()...)
		for path := range w.Wrote() {
			written[path] = true
		}
		for path := range w.Unchanged() {
			unchanged[path] = true
		}
		for path := range w.WouldChange() {
			wouldChange[path] = true
		}
		if aerr != nil {
			return reports, written, unchanged, wouldChange, fmt.Errorf("apply %s: %w", name, aerr)
		}
	}
	return reports, written, unchanged, wouldChange, nil
}
