package cli

import "testing"

// TestEditorArgv covers the regression where a whitespace-only $EDITOR made
// strings.Fields return an empty slice and the indexing panicked. It must fall
// back to vi, and split flags out of the executable path.
func TestEditorArgv(t *testing.T) {
	cases := []struct {
		env  string
		want []string
	}{
		{"", []string{"vi", "/f"}},
		{"   ", []string{"vi", "/f"}},
		{"\t", []string{"vi", "/f"}},
		{"nano", []string{"nano", "/f"}},
		{"code --wait", []string{"code", "--wait", "/f"}},
		{"  vim -u NONE ", []string{"vim", "-u", "NONE", "/f"}},
	}
	for _, tc := range cases {
		got := editorArgv(tc.env, "/f")
		if len(got) != len(tc.want) {
			t.Errorf("editorArgv(%q) = %v, want %v", tc.env, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("editorArgv(%q) = %v, want %v", tc.env, got, tc.want)
				break
			}
		}
	}
}

// TestValidateMCPID_RejectsDegenerate mirrors source.ValidateComponentID: a
// lone "." or an all-whitespace id passes the separator/traversal check but
// would author mcp/..toml or "mcp/ .toml" — confusing artifacts no later
// command can address cleanly. The CLI gate should reject them early with a
// clear message rather than defer to the write boundary.
func TestValidateMCPID_RejectsDegenerate(t *testing.T) {
	bad := []string{"", ".", " ", "   ", "\t", "a/b", "a\\b", "a..b"}
	for _, s := range bad {
		if err := validateMCPID(s); err == nil {
			t.Errorf("validateMCPID(%q) = nil; want error", s)
		}
	}
	good := []string{"github", "my-server", "a.b.c", "srv1"}
	for _, s := range good {
		if err := validateMCPID(s); err != nil {
			t.Errorf("validateMCPID(%q) = %v; want nil", s, err)
		}
	}
}

// TestDeriveMarketplaceSlug_NeverEmpty proves the empty-fallback runs AFTER
// sanitisation: a punctuation-only source like "..." sanitises to "" and must
// still yield the usable "marketplace" slug, not "" (which would author
// marketplaces/.toml and a marketplaces/_ cache dir).
func TestDeriveMarketplaceSlug_NeverEmpty(t *testing.T) {
	for _, in := range []string{"...", ".", "//", "   ", "https://"} {
		if got := deriveMarketplaceSlug(in); got == "" {
			t.Errorf("deriveMarketplaceSlug(%q) = %q; want non-empty", in, got)
		}
	}
	// A normal source still derives a sensible, non-empty slug.
	if got := deriveMarketplaceSlug("https://github.com/obra/superpowers.git"); got == "" {
		t.Errorf("deriveMarketplaceSlug of a real URL returned empty")
	}
}
