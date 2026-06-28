package opencode

import "github.com/spxrogers/agentsync/internal/adapter"

// VersionRoots implements adapter.VersionedDirs: ~/.config/opencode (its config
// dir) plus ~/.claude/skills (the shared Claude skills dir OpenCode also writes
// to). The apply tail de-nests + de-dups across adapters, so when Claude is also
// enabled, ~/.claude/skills is captured by Claude's ~/.claude repo instead of
// getting its own. Returns nil at project scope.
func (a *Adapter) VersionRoots(scope adapter.Scope, project string) []string {
	if scope != adapter.ScopeUser {
		return nil
	}
	p := ResolvePaths(a.opts.TargetRoot, "", false)
	return nonEmptyDirs(p.ConfigDir, p.ClaudeSkillsDir)
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
