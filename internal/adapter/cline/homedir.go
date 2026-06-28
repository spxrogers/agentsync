package cline

import "github.com/spxrogers/agentsync/internal/adapter"

// HomeDir implements adapter.VersionedHome: the user-scope ~/.cline config dir is
// the local-only git-backup root (issue #118). Returns ("", false) at project
// scope. (At user scope Cline only writes ~/.cline/mcp.json; its global rules live
// in a non-XDG app path agentsync does not target.)
func (a *Adapter) HomeDir(scope adapter.Scope, project string) (string, bool) {
	if scope != adapter.ScopeUser {
		return "", false
	}
	dir := ResolvePaths(a.opts.TargetRoot, "", false).ConfigDir
	if dir == "" {
		return "", false
	}
	return dir, true
}
