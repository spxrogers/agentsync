package cli

import (
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// TestVersionedDirsContract pins the git-backup capability in code so it can't
// silently drift: every registered adapter (deep AND breadth) must declare its
// user-scope version roots as absolute dirs under the target root and abstain
// (nil) at project scope. The unit is the directory — shared dirs are deduped by
// the apply tail, not the adapter.
func TestVersionedDirsContract(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTSYNC_TARGET_ROOT", root)

	reg := registryFactory()
	for _, name := range reg.Names() {
		ad := reg.Lookup(name)
		vd, ok := ad.(adapter.VersionedDirs)
		if !ok {
			t.Errorf("adapter %q does not implement VersionedDirs", name)
			continue
		}
		// Some breadth agents are project-scope only (cloud IDEs, project-only
		// memory) and correctly declare NO user-scope root. What we pin: whatever is
		// returned is a valid absolute dir under the target root, never $HOME.
		dirs := vd.VersionRoots(adapter.ScopeUser, "")
		for _, d := range dirs {
			if !filepath.IsAbs(d) {
				t.Errorf("%s.VersionRoots(user) = %q; want absolute", name, d)
			}
			// Path-BOUNDARY check, not a byte prefix: a sibling like `<root>-evil`
			// starts with the root string but is not under it. isUnderDir uses
			// filepath.Rel and rejects such escapes (the exact bug class this guard
			// exists to catch). See TestVersionedDirsContract_RejectsSibling.
			if !isUnderDir(d, root) {
				t.Errorf("%s.VersionRoots(user) = %q; want under target root %q", name, d, root)
			}
			if d == root {
				t.Errorf("%s.VersionRoots(user) returned the bare $HOME root %q; agentsync must never init a repo at $HOME", name, d)
			}
		}
		// Project scope abstains.
		if pdirs := vd.VersionRoots(adapter.ScopeProject, filepath.Join(root, "proj")); len(pdirs) != 0 {
			t.Errorf("%s.VersionRoots(project) = %v; want nil", name, pdirs)
		}
	}

	// Codex declares the shared cross-vendor skills dir.
	agentsSkills := filepath.Join(root, ".agents", "skills")
	if !contains(reg.Lookup("codex").(adapter.VersionedDirs).VersionRoots(adapter.ScopeUser, ""), agentsSkills) {
		t.Errorf("codex VersionRoots should include the shared %s", agentsSkills)
	}
}

// fakeVersionedDirs is a stand-in adapter returning caller-supplied roots.
type fakeVersionedDirs struct{ roots []string }

func (f fakeVersionedDirs) VersionRoots(adapter.Scope, string) []string { return f.roots }

// TestVersionedDirsContract_RejectsSibling proves the contract's boundary check
// catches the prefix-bug class: a returned root whose STRING starts with the
// target root but is a sibling directory (e.g. `<root>-evil/x`) is not under it.
// The old strings.HasPrefix check would have accepted it; isUnderDir rejects it.
func TestVersionedDirsContract_RejectsSibling(t *testing.T) {
	root := t.TempDir()
	sibling := filepath.Join(root+"-evil", "x")
	var vd adapter.VersionedDirs = fakeVersionedDirs{roots: []string{sibling}}
	for _, d := range vd.VersionRoots(adapter.ScopeUser, "") {
		// It genuinely shares root's byte prefix (what the old check keyed on)...
		if len(d) < len(root) || d[:len(root)] != root {
			t.Fatalf("fixture bug: %q should share the byte prefix %q", d, root)
		}
		// ...yet the boundary check must reject it as not-under-root.
		if isUnderDir(d, root) {
			t.Fatalf("boundary check accepted sibling %q as under %q — the prefix-bug class is not caught", d, root)
		}
	}
}

// TestEnabledVersionRoots_DedupAndDenest proves the apply-tail aggregation: a
// shared dir declared by multiple agents appears once, and a nested dir is dropped
// in favor of its ancestor.
func TestEnabledVersionRoots_DedupAndDenest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTSYNC_TARGET_ROOT", root)
	reg := registryFactory()

	// codex + warp both write to ~/.agents/skills → it must appear exactly once.
	roots := enabledVersionRoots(reg, []string{"codex", "warp"}, adapter.ScopeUser, "")
	agentsSkills := filepath.Join(root, ".agents", "skills")
	if n := countEq(roots, agentsSkills); n != 1 {
		t.Errorf("shared %s appears %d times across codex+warp roots, want 1: %v", agentsSkills, n, roots)
	}

	// claude + opencode: opencode declares ~/.claude/skills, nested under claude's
	// ~/.claude → de-nested away (claude's repo captures it).
	roots = enabledVersionRoots(reg, []string{"claude", "opencode"}, adapter.ScopeUser, "")
	claudeSkills := filepath.Join(root, ".claude", "skills")
	if contains(roots, claudeSkills) {
		t.Errorf("~/.claude/skills should be de-nested under ~/.claude, but appears in %v", roots)
	}
	if !contains(roots, filepath.Join(root, ".claude")) {
		t.Errorf("~/.claude should be a root: %v", roots)
	}
	// No root may be nested under another.
	for i := range roots {
		for j := range roots {
			if i != j && isUnderDir(roots[i], roots[j]) {
				t.Errorf("root %q is nested under %q — de-nesting failed", roots[i], roots[j])
			}
		}
	}
}

// TestVersionRootOwners_SharedDir proves the shared-dir blast-radius detection:
// ~/.agents/skills is owned by both codex and warp, which the revert path warns on.
func TestVersionRootOwners_SharedDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTSYNC_TARGET_ROOT", root)
	reg := registryFactory()
	owners := versionRootOwners(reg, []string{"codex", "warp"}, adapter.ScopeUser, "")
	agentsSkills := filepath.Join(root, ".agents", "skills")
	got := owners[agentsSkills]
	if !contains(got, "codex") || !contains(got, "warp") {
		t.Fatalf("owners[%s] = %v, want both codex and warp", agentsSkills, got)
	}
	// codex's own ~/.codex is single-owner.
	if o := owners[filepath.Join(root, ".codex")]; len(o) != 1 || o[0] != "codex" {
		t.Errorf("owners[~/.codex] = %v, want [codex]", o)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func countEq(ss []string, want string) int {
	n := 0
	for _, s := range ss {
		if s == want {
			n++
		}
	}
	return n
}

// TestOwnersFor_RecoversFoldedRoot is the fix for Problem 3 (issue #154): an
// agent's own de-nested root can be a child parent-folded into an ancestor key in
// the global owners map, so an exact-key lookup drops the cross-agent warning.
// OpenCode's ~/.claude/skills folds into Claude's ~/.claude under {claude,
// opencode}; ownersFor must recover the ancestor's owner set (incl. claude) rather
// than nil.
func TestOwnersFor_RecoversFoldedRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTSYNC_TARGET_ROOT", root)
	reg := registryFactory()

	owners := versionRootOwners(reg, []string{"claude", "opencode"}, adapter.ScopeUser, "")
	claudeSkills := filepath.Join(root, ".claude", "skills") // opencode's own de-nested root

	// Precondition: the child root is folded away — it is NOT an exact key.
	if _, ok := owners[claudeSkills]; ok {
		t.Fatalf("precondition: %s should be folded into ~/.claude, not a global key", claudeSkills)
	}
	// The exact-key path would return nil (the latent bug); ownersFor recovers it.
	if got := owners[claudeSkills]; got != nil {
		t.Fatalf("owners[foldedRoot] unexpectedly non-nil: %v", got)
	}
	got := ownersFor(owners, claudeSkills)
	if got == nil {
		t.Fatal("ownersFor returned nil for a parent-folded root — the shared-dir warning would be dropped")
	}
	if !contains(got, "claude") {
		t.Fatalf("ownersFor(%s) = %v, want to include claude via nearest ancestor ~/.claude", claudeSkills, got)
	}
}
