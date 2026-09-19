package paths

import "testing"

// TestNormalizeDir_CaseFold exercises the platform case-fold (of the RESOLVED
// predicates only; the lexical ContainsDir never folds) on every OS by
// flipping the switch directly: CI's Linux legs would otherwise never run the
// ToLower branch, and a silently deleted fold is exactly the kind of guard
// regression that only shows up on a macOS user's machine as a repo at $HOME.
// Pure-unit — the paths need not exist.
func TestNormalizeDir_CaseFold(t *testing.T) {
	saved := caseInsensitiveFS
	t.Cleanup(func() { caseInsensitiveFS = saved })

	caseInsensitiveFS = true
	if !SameDirResolved("/Users/Alice", "/users/alice") {
		t.Fatal("with the fold on, /Users/Alice and /users/alice must be one directory")
	}
	if !ContainsDirResolved("/Users/Alice", "/users/alice/.grok") {
		t.Fatal("with the fold on, containment must ignore case too")
	}

	caseInsensitiveFS = false
	if SameDirResolved("/Users/Alice", "/users/alice") {
		t.Fatal("with the fold off, spellings that differ in case are different directories")
	}
	if ContainsDirResolved("/Users/Alice", "/users/alice/.grok") {
		t.Fatal("with the fold off, containment must be case-sensitive")
	}
}
