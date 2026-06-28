package cline

import "github.com/spxrogers/agentsync/internal/adapter"

// VersionRoots implements adapter.VersionedDirs: the user-scope ~/.cline config dir
// is the local-only git-backup root (issue #118). Returns nil at project scope.
// (At user scope Cline only writes ~/.cline/mcp.json; its global rules live in a
// non-XDG app path agentsync does not target.)
func (a *Adapter) VersionRoots(scope adapter.Scope, project string) []string {
	if scope != adapter.ScopeUser {
		return nil
	}
	p := ResolvePaths(a.opts.TargetRoot, "", false)
	return adapter.NonEmptyDirs(p.ConfigDir)
}
