package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// deepVersionedAgents are the deep adapters whose single, coherent user-scope
// config dir agentsync git-backs-up (issue #118). The breadth-tier (generic)
// agents deliberately ABSTAIN: their user-scope targets are scattered across
// multiple top-level dirs and shared cross-agent locations (e.g. ~/.agents/skills),
// so there is no safe single dir to `git init` — versioning them is an accepted
// gap, not a silent drop. See docs/architecture.md and the spec.
var deepVersionedAgents = map[string]bool{
	"claude": true, "opencode": true, "codex": true, "cursor": true,
	"gemini": true, "continue": true, "windsurf": true, "roo": true, "cline": true,
}

// TestVersionedHomeContract pins the git-backup capability in code so it can't
// silently drift: every DEEP adapter must expose a valid user-scope dir under the
// target root and abstain at project scope, and every BREADTH-tier adapter must
// NOT implement VersionedHome at all. The analogue of TestEveryAdapterClassifiesSkips.
func TestVersionedHomeContract(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTSYNC_TARGET_ROOT", root)

	reg := registryFactory()
	seenDeep := map[string]bool{}
	for _, name := range reg.Names() {
		ad := reg.Lookup(name)
		vh, ok := ad.(adapter.VersionedHome)

		if !deepVersionedAgents[name] {
			// Breadth tier: must abstain.
			if ok {
				t.Errorf("breadth-tier agent %q implements VersionedHome; the generic tier must abstain (no safe single config dir to git-init)", name)
			}
			continue
		}
		seenDeep[name] = true

		if !ok {
			t.Errorf("deep agent %q does not implement VersionedHome", name)
			continue
		}
		// User scope: a real, absolute dir under the target root.
		dir, has := vh.HomeDir(adapter.ScopeUser, "")
		if !has || dir == "" {
			t.Errorf("%s.HomeDir(user) = (%q, %v); want a non-empty dir", name, dir, has)
			continue
		}
		if !filepath.IsAbs(dir) {
			t.Errorf("%s.HomeDir(user) = %q; want an absolute path", name, dir)
		}
		if !strings.HasPrefix(dir, root) {
			t.Errorf("%s.HomeDir(user) = %q; want it under target root %q", name, dir, root)
		}
		// Project scope: must abstain (project dirs live in the user's own repo).
		if pdir, phas := vh.HomeDir(adapter.ScopeProject, filepath.Join(root, "proj")); phas || pdir != "" {
			t.Errorf("%s.HomeDir(project) = (%q, %v); want (\"\", false)", name, pdir, phas)
		}
	}

	for name := range deepVersionedAgents {
		if !seenDeep[name] {
			t.Errorf("deep agent %q expected in the registry but not found", name)
		}
	}
}
