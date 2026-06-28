package opencode

import "github.com/spxrogers/agentsync/internal/adapter"

// HomeDir implements adapter.VersionedHome: the user-scope ~/.config/opencode
// config dir is the local-only git-backup root (issue #118). Returns ("", false)
// at project scope. (OpenCode also writes user skills under ~/.claude/skills,
// which the Claude repo captures; nothing is lost.)
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
