package marketplace

import "testing"

// TestLoadComponentEntry_RejectsTraversalName closes the divergence between
// the marketplace projection and the source loader twin: the loader validates
// component names (a name becomes a path segment at render time, so a hostile
// plugin frontmatter "name: ../../evil" is zip-slip-by-name), but the
// marketplace projection's loaders did not. marketplace.Project has no
// production caller today (the runtime path projects via the validating source
// loader), but it is exported API and the two paths must not diverge.
func TestLoadComponentEntry_RejectsTraversalName(t *testing.T) {
	readFile := func(string) ([]byte, error) {
		return []byte("---\nname: ../../evil\n---\nbody\n"), nil
	}
	if _, err := loadMarkdownEntry("cmd.md", readFile); err == nil {
		t.Fatal("expected loadMarkdownEntry to reject a traversal name")
	}
	if _, err := loadSkillEntry("skills/x", readFile); err == nil {
		t.Fatal("expected loadSkillEntry to reject a traversal name")
	}
}

// TestLoadComponentEntry_AllowsNormalName confirms the guard does not reject a
// legitimate component name.
func TestLoadComponentEntry_AllowsNormalName(t *testing.T) {
	readFile := func(string) ([]byte, error) {
		return []byte("---\nname: code-review\n---\nbody\n"), nil
	}
	if _, err := loadMarkdownEntry("cmd.md", readFile); err != nil {
		t.Fatalf("legitimate name rejected: %v", err)
	}
}
