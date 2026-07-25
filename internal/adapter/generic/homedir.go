package generic

import (
	"path/filepath"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// VersionRoots implements adapter.VersionedDirs: the set of top-level directories
// this breadth-tier agent writes into at user scope, so the apply tail can version
// them as a local-only rollback history (issue #118). Derived from the Spec's
// user-scope targets. The apply tail unions, de-nests, and de-dups these across all
// enabled adapters, so a shared cross-agent dir (e.g. ~/.agents/skills, which many
// breadth agents and Codex all target) is versioned exactly once. Returns nil at
// project scope.
func (a *Adapter) VersionRoots(scope adapter.Scope, project string) []string {
	if scope != adapter.ScopeUser {
		return nil
	}
	seen := map[string]bool{}
	var roots []string
	add := func(rel string) {
		if rel == "" {
			return
		}
		root := versionRootOf(a.opts.TargetRoot, rel)
		if root == "" || seen[root] {
			return
		}
		seen[root] = true
		roots = append(roots, root)
	}
	add(a.spec.Memory.User)
	add(a.spec.MCP.User)
	add(a.spec.Skills.User)
	return roots
}

// versionRootOf maps a user-scope relative target path to the agent's top-level
// version-root directory under targetRoot. Most breadth agents live under a single
// dotted segment (~/.qwen); a known set of multi-segment bases keeps two segments
// (~/.config/<x>, ~/.aws/<x>, ~/.agents/skills, ~/.pi/agent, ~/.gemini/config) so a
// shared dir resolves to a stable shared root. Returns "" for a bare $HOME-level
// file — agentsync never inits a repo at $HOME.
//
// WHOLE-IDE-DIR VERSIONING TRADEOFF (design note, issue #175): several roots this
// resolves to are directories the IDE fills with its OWN state — zed's files map to
// ~/.config/zed, kiro's steering to ~/.kiro — so the local-rollback repo
// (adapter.VersionedDirs, issue #118) inits `.git` AMID unrelated app state. That is
// deliberate and its blast radius is bounded by design: only agentsync-WRITTEN files
// are ever tracked (the apply tail stages exactly managedRelsUnder(root, planned),
// StageTrackedDeletions touches only already-tracked files, and go-git's HardReset
// runs no `git clean`), so untracked IDE files are never committed, reset, or deleted —
// the repo simply co-exists with them. The alternative (a narrower per-file root) was
// declined: it fights the shared-dir dedup and would fragment the rollback history.
func versionRootOf(targetRoot, rel string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(rel)), "/")
	if len(parts) < 2 {
		return "" // a bare file directly in $HOME is not versionable
	}
	if parts[0] == ".." || parts[0] == "." || parts[0] == "" {
		return "" // defense-in-depth: a target must stay under $HOME, never escape it
	}
	// SYNC OBLIGATION (issue #154): this multi-segment base allowlist MUST stay in
	// sync with internal/adapter/generic/specs.go. Any new breadth spec whose
	// user target lives under a *multi-product* base (a base shared by more than one
	// product/agent — e.g. `.local/share/<x>`, `.var/app/<x>`) MUST be added here,
	// or it collapses to a single over-broad segment (e.g. ~/.local) and would
	// version a huge shared tree under one repo. TestVersionRoots_AllSpecs and
	// TestVersionRootOf_Tightness (homedir_test.go) guard this.
	switch parts[0] {
	case ".config", ".aws", ".agents", ".pi", ".gemini":
		return filepath.Join(targetRoot, parts[0], parts[1])
	default:
		return filepath.Join(targetRoot, parts[0])
	}
}
