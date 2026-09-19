package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestDenestRoots_SymlinkedParentFirstApply pins the round-2 regression fix
// (issue #270): when the PARENT root reaches its directory through a symlink and
// the nested root does not exist yet (a first apply — the apply tail computes
// roots before any write), the nested root must still fold into the parent.
// With a naive "resolve only what exists" normalization the two spellings land
// in different trees, both survive de-nesting, and apply inits a repo inside a
// repo. paths.ContainsDir resolves through the deepest EXISTING ancestor for
// exactly this case.
func TestDenestRoots_SymlinkedParentFirstApply(t *testing.T) {
	testenv.RequireContainer(t)
	base := t.TempDir()
	real := filepath.Join(base, "real-claude")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "dot-claude") // ~/.claude → /data/claude
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	child := filepath.Join(link, "skills") // does NOT exist yet

	if !paths.ContainsDir(link, child) {
		t.Fatalf("ContainsDir(%q, %q) = false for a not-yet-created child under a symlinked parent", link, child)
	}
	if !paths.ContainsDir(real, child) {
		t.Fatalf("ContainsDir(%q, %q) = false; the child must resolve through the link into the real tree", real, child)
	}
	if got := denestRoots([]string{child, link}); !reflect.DeepEqual(got, []string{link}) {
		t.Fatalf("denestRoots = %v; want the child folded into the symlinked parent [%s]", got, link)
	}
	// And the two spellings of the parent itself are one root.
	if got := denestRoots([]string{real, link}); len(got) != 1 {
		t.Fatalf("denestRoots(real, link) = %v; want one root — they are the same directory", got)
	}
}

// TestDenestRoots_ChildSymlinkedOut pins the deliberate behaviour change that
// came with resolving symlinks in containment: a child root that is itself a
// symlink OUT of its parent (`~/.claude/skills → /data/skills`) is no longer
// de-nested. git does not follow symlinks into directories, so a repo at the
// target is not a repo inside the parent's — and before this change the
// target's contents were never versioned at all (the parent repo tracked only
// the link).
func TestDenestRoots_ChildSymlinkedOut(t *testing.T) {
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
	if err := os.Symlink(target, child); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	if paths.ContainsDir(parent, child) {
		t.Fatalf("ContainsDir(%q, %q) = true; a symlink out of the parent is a different directory", parent, child)
	}
	got := denestRoots([]string{parent, child})
	if !reflect.DeepEqual(got, []string{parent, child}) {
		t.Fatalf("denestRoots = %v; want both roots kept — the child lives outside the parent's tree", got)
	}
}

// TestDenestRoots_LaterSortingAncestor pins that de-nesting checks every root
// against every other, not only against roots already kept: after symlink
// resolution a root that SORTS later can be the ancestor of one that sorts
// earlier, and a single forward pass would keep the nested one.
func TestDenestRoots_LaterSortingAncestor(t *testing.T) {
	testenv.RequireContainer(t)
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// "a-child" sorts BEFORE "z-parent", but resolves to a dir under z-parent's.
	aChild := filepath.Join(base, "a-child")
	zParent := filepath.Join(base, "z-parent")
	if err := os.Symlink(filepath.Join(real, "sub"), aChild); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	if err := os.Symlink(real, zParent); err != nil {
		t.Fatal(err)
	}
	if got := denestRoots([]string{aChild, zParent}); !reflect.DeepEqual(got, []string{zParent}) {
		t.Fatalf("denestRoots = %v; want only the ancestor [%s]", got, zParent)
	}
}
