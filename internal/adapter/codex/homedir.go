package codex

import "github.com/spxrogers/agentsync/internal/adapter"

// VersionRoots implements adapter.VersionedDirs: ~/.codex (its config dir) plus
// ~/.agents/skills (the shared cross-vendor Agent-Skills dir Codex and several
// breadth agents all write to). The apply tail de-dups ~/.agents/skills so it is
// versioned exactly once regardless of how many agents target it. Returns nil at
// project scope.
func (a *Adapter) VersionRoots(scope adapter.Scope, project string) []string {
	if scope != adapter.ScopeUser {
		return nil
	}
	p := ResolvePaths(a.opts.TargetRoot, "", false)
	return adapter.NonEmptyDirs(p.ConfigDir, p.SkillsDir)
}
