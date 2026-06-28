package roo

import "github.com/spxrogers/agentsync/internal/adapter"

// VersionRoots implements adapter.VersionedDirs: the user-scope ~/.roo config dir
// is the local-only git-backup root (issue #118). Returns nil at project scope.
func (a *Adapter) VersionRoots(scope adapter.Scope, project string) []string {
	if scope != adapter.ScopeUser {
		return nil
	}
	p := ResolvePaths(a.opts.TargetRoot, "", false)
	return nonEmptyDirs(p.ConfigDir)
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
