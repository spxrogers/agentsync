package codex

import "github.com/spxrogers/agentsync/internal/adapter"

// HomeDir implements adapter.VersionedHome: the user-scope ~/.codex config dir is
// the local-only git-backup root (issue #118). Returns ("", false) at project
// scope. (Codex skills live in the shared ~/.agents/skills dir, OUTSIDE ~/.codex,
// so they are not versioned here — an accepted cross-agent gap, like Claude's
// ~/.claude.json; they keep relying on .state/backups.)
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
