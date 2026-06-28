package continuedev

import "github.com/spxrogers/agentsync/internal/adapter"

// HomeDir implements adapter.VersionedHome: the user-scope ~/.continue config dir
// is the local-only git-backup root (issue #118). Returns ("", false) at project
// scope.
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
