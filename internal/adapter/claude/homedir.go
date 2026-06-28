package claude

import "github.com/spxrogers/agentsync/internal/adapter"

// VersionRoots implements adapter.VersionedDirs: the user-scope ~/.claude config
// dir is the local-only git-backup root (issue #118). Returns nil at project
// scope — project destinations live in the user's own repo. Note ~/.claude.json
// (written at $HOME, OUTSIDE ~/.claude) is intentionally NOT versioned; only this
// dir is — it keeps relying on the existing .state/backups foreign-collision
// backup. (~/.claude/skills, which OpenCode also writes to, is captured here.)
func (a *Adapter) VersionRoots(scope adapter.Scope, project string) []string {
	if scope != adapter.ScopeUser {
		return nil
	}
	p := ResolvePaths(a.opts.TargetRoot, "", false)
	return nonEmptyDirs(p.Home)
}

// nonEmptyDirs returns the non-empty arguments as a slice.
func nonEmptyDirs(dirs ...string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}
