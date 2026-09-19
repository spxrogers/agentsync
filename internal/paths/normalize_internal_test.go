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

// TestResolveExistingPrefix_RelativePassesThrough pins the relative-path
// contract: the walk reaches ".", which is not a prefix of the input, and the
// path is returned unchanged rather than having its first byte sliced off (a
// bug an earlier revision had). Pure-unit — nothing here exists on disk.
func TestResolveExistingPrefix_RelativePassesThrough(t *testing.T) {
	// ".claude/skills" is the sharp row: "." IS a byte prefix of it, so a naive
	// prefix check would anchor on the cwd and eat the leading dot.
	for _, p := range []string{"foo", "foo/bar", ".claude/skills", "nosuchdir", "."} {
		if got := resolveExistingPrefix(p); got != p {
			t.Errorf("resolveExistingPrefix(%q) = %q; want the input unchanged", p, got)
		}
	}
	// Two distinct relative dirs must never normalize to one string.
	if SameDirResolved("afoo", "bfoo") {
		t.Fatal("distinct relative paths normalized to the same string")
	}
}

func TestIsComponentPrefix(t *testing.T) {
	cases := []struct {
		p, prefix string
		want      bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a", true},
		{"/a/b", "/", true},
		{"/ab", "/a", false},
		{".claude", ".", false},
		{".", ".", true},
		{"foo/bar", "foo", true},
		{"foo/bar", ".", false},
	}
	for _, tc := range cases {
		if got := isComponentPrefix(tc.p, tc.prefix); got != tc.want {
			t.Errorf("isComponentPrefix(%q, %q) = %v, want %v", tc.p, tc.prefix, got, tc.want)
		}
	}
}
