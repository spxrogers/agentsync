package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestContainsDirResolved_PendingChildUnderSymlinkedParent pins the resolved
// predicate's symmetry (issue #270, #271 review round 2). It exercises only the
// internal/paths API but lives here because it needs real symlinks, and
// internal/paths is in the host test-fast set with no container guard. A path
// that does not exist yet under a parent that reaches its directory through a
// symlink must resolve through the deepest EXISTING ancestor, so the pending
// child and the real tree agree. A naive "resolve only what exists" would put
// the two in different trees. The $HOME guard relies on this whenever a declared
// root under a symlinked home has not been created yet.
func TestContainsDirResolved_PendingChildUnderSymlinkedParent(t *testing.T) {
	testenv.RequireContainer(t)
	base := t.TempDir()
	real := filepath.Join(base, "real-claude")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "dot-claude") // ~/.claude → /data/claude
	mustSymlink(t, real, link)
	child := filepath.Join(link, "skills") // does NOT exist yet

	if !paths.ContainsDirResolved(real, child) {
		t.Fatalf("ContainsDirResolved(%q, %q) = false; the pending child must resolve through the link into the real tree", real, child)
	}
	if !paths.SameDirResolved(real, link) {
		t.Fatalf("SameDirResolved(%q, %q) = false; a symlink and its target are one directory", real, link)
	}
	// The lexical predicate, by contrast, sees only spellings.
	if paths.ContainsDir(real, child) {
		t.Fatalf("ContainsDir(%q, %q) = true; the lexical predicate must not resolve symlinks", real, child)
	}
}

// TestDenestRoots_IsLexical pins the de-nesting decision the #271 review settled:
// containment for git-backup topology follows the DECLARED spelling. A child root
// that is a symlink out of its parent (`~/.claude/skills → /data/skills`) folds
// into the parent like a real subdirectory — a separate repo at the link's target
// would never be opened (agit.Detect walks the link spelling into the parent's
// .git first) and would make the parent un-revertable through the nested-repo
// probe. And two roots that are the same directory under different spellings
// are NOT de-duplicated here (that is identity, not topology); the byte-exact
// dedup upstream and Detect's filesystem walk make that harmless.
func TestDenestRoots_IsLexical(t *testing.T) {
	testenv.RequireContainer(t)
	base := t.TempDir()
	parent := filepath.Join(base, "dot-claude")
	target := filepath.Join(base, "data-skills")
	for _, d := range []string{parent, target} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	child := filepath.Join(parent, "skills")
	mustSymlink(t, target, child)
	if got := denestRoots([]string{child, parent}); !reflect.DeepEqual(got, []string{parent}) {
		t.Fatalf("denestRoots = %v; want the symlinked-out child folded into its parent [%s]", got, parent)
	}
	// A not-yet-created child folds too (no filesystem access is involved at all).
	pending := filepath.Join(parent, "commands")
	if got := denestRoots([]string{pending, parent}); !reflect.DeepEqual(got, []string{parent}) {
		t.Fatalf("denestRoots = %v; want the pending child folded into [%s]", got, parent)
	}
	// Two spellings of one directory stay two roots here: lexical means lexical.
	// The link is REAL, so the resolved predicate would fold them — this row is
	// what distinguishes the two predicates.
	alias := filepath.Join(base, "link-to-parent")
	mustSymlink(t, parent, alias)
	if got := denestRoots([]string{parent, alias}); len(got) != 2 {
		t.Fatalf("denestRoots = %v; want both spellings kept by the lexical pass", got)
	}
}
