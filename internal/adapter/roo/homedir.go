package roo

import "github.com/spxrogers/agentsync/internal/adapter"

// VersionRoots implements adapter.VersionedDirs: the user-scope ~/.roo config dir
// is the local-only git-backup root (issue #118). Returns nil at project scope.
func (a *Adapter) VersionRoots(scope adapter.Scope, project string) []string {
	if scope != adapter.ScopeUser {
		return nil
	}
	p := ResolvePaths(a.opts.TargetRoot, "", false)
	return adapter.NonEmptyDirs(p.ConfigDir)
}
