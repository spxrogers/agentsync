package claude

import "github.com/spxrogers/agentsync/internal/adapter"

// HomeDir implements adapter.VersionedHome: the user-scope ~/.claude config dir is
// the local-only git-backup root (issue #118). Returns ("", false) at project
// scope — project destinations live in the user's own repo. Note ~/.claude.json
// (written at $HOME, OUTSIDE ~/.claude) is intentionally NOT versioned; only this
// dir is — it keeps relying on the existing .state/backups foreign-collision
// backup. See docs/architecture.md.
func (a *Adapter) HomeDir(scope adapter.Scope, project string) (string, bool) {
	if scope != adapter.ScopeUser {
		return "", false
	}
	home := ResolvePaths(a.opts.TargetRoot, "", false).Home
	if home == "" {
		return "", false
	}
	return home, true
}
